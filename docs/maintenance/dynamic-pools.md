# Dynamic resource pools

The first dynamic-pool milestone supports append-only, static pools on the agent
resource manager. It lets an administrator add a pool without restarting the
master. It does not create infrastructure: agents must still be started and
configured to join the new pool.

## Supported behavior

A dynamic pool has durable desired configuration and one of three operation
states:

- `Pending`: the configuration is saved, but runtime initialization has not
  completed.
- `Ready`: the runtime pool is published for agent admission and task
  scheduling.
- `Failed`: initialization failed. The pool remains unavailable until an
  administrator explicitly retries it.

Only `Ready` pools appear through the regular resource-pool listing and can
accept agents or tasks. Dynamic desired configuration is loaded before agent
state restoration after a master restart. The master refuses to start if a
saved configuration is corrupt, uses an unsupported schema version, belongs to
a non-agent resource manager, or collides with any pool configured in YAML.
This fail-closed behavior avoids deleting or misrouting saved agent state.

Pool names are global across resource managers because normal task admission
routes by pool name. Dynamic names must match
`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`. A dynamic pool cannot be renamed, updated,
or deleted. This milestone does not change default pools, move agents or
allocations, or support provider-backed, Kubernetes, Slurm, or PBS pools.

## REST API

These endpoints require an authenticated session cookie or bearer token. Create
and retry require the same authorization as updating master configuration; list
requires authorization to read master configuration. With basic authorization,
these permissions are restricted to administrators.

Create a pool with:

```text
POST /api/v1/resource-pools/dynamic
Content-Type: application/json

{
  "cluster_name": "agent-cluster",
  "idempotency_key": "pool-create-2026-09-20-a",
  "config": {
    "pool_name": "batch-a",
    "description": "Static agents for batch work",
    "max_aux_containers_per_agent": 100
  }
}
```

`cluster_name` may be omitted when exactly one agent resource manager is
configured. The request body is limited to 1 MiB and rejects unknown fields.
`provider` is rejected even when it is otherwise valid master configuration.

The master fills in the effective scheduler and task container defaults and
saves that normalized configuration before runtime initialization. This keeps
the pool's behavior stable if YAML defaults later change. A new operation
returns `201`; an exact replay with the same cluster-scoped idempotency key,
pool name, configuration, and config version returns `200` and the original
operation. Reusing the key for a different request or using an occupied pool
name returns `409`.

The response has this form:

```json
{
  "cluster_name": "agent-cluster",
  "pool_name": "batch-a",
  "config_version": 1,
  "config": {},
  "state": "Ready",
  "created_at": "2026-09-20T08:00:00Z",
  "updated_at": "2026-09-20T08:00:00Z"
}
```

The actual `config` contains the full effective resource-pool configuration.
Registry passwords and tokens are redacted in API responses. Provider
credentials cannot be submitted. A master-owned worker initializes saved
`Pending` pools. A create or retry response can contain `Pending`; poll the list
endpoint until the operation becomes `Ready` or `Failed`. The worker rescans
saved operations periodically, so an insert committed just before a failed
read or canceled request is still advanced without restarting the master.
It also reconstructs a `Ready` pool whose runtime was not published after an
ambiguous database write. Reconstruction uses the saved effective configuration
and retries transient runtime failures. `Failed` pools still require explicit
retry.
Exact replays return the current operation state and do not start another
runtime pool.

List all operations, or operations for one resource manager, with:

```text
GET /api/v1/resource-pools/dynamic
GET /api/v1/resource-pools/dynamic?cluster_name=agent-cluster
```

The response is `{"resource_pools":[...]}` in creation order. This status API
includes non-ready desired pools; the normal resource-pool API includes only
ready pools.

Retry a failed operation with an empty body:

```text
POST /api/v1/resource-pools/dynamic/batch-a/retry?cluster_name=agent-cluster
```

Retry always uses the saved effective configuration. Retrying a `Pending` or
`Ready` operation returns `409`; a missing pool returns `404`.

## Restart and rollback limits

On startup, all saved desired configurations are validated before persisted
agent statistics are cleaned up. The agent resource manager reconstructs every
saved pool before accepting restored or new agents. A successful reconstruction
sets the durable state to `Ready`. If reconstruction fails, master startup stops
rather than running with a partial set of pools. An administrator must correct
the underlying cause before restarting; the live retry endpoint is available
only while the master is running.

The database migration has a rollback migration for release rollback tooling.
Rolling it back drops the dynamic pool table and all saved desired
configurations. Back up the table and stop the master before an intentional
rollback if those records must be preserved. A release that predates dynamic
pools cannot restore or schedule those pools.

The CPU-agent acceptance at `c11dcbcec` passed creation and idempotent replay,
permission denials, continued original-pool work with the same allocation and
real Docker container, new-pool work, and recovery after master restart. See
[validation](validation.md) for the exact evidence. The broader fault-injection
and GPU cases in [Append-only online resource pools](online-resource-pools.md)
remain separate gates.

## CLI

Save the resource-pool configuration itself as YAML or JSON. For example,
`batch-a.yaml` can contain:

```yaml
pool_name: batch-a
description: Static agents for batch work
max_aux_containers_per_agent: 100
```

Create the pool with a stable idempotency key:

```sh
det resource-pool create batch-a.yaml \
  --idempotency-key pool-create-2026-09-20-a \
  --cluster-name agent-cluster
```

Omit `--cluster-name` when only one agent resource manager is configured. Add
`--json` to print the complete operation record. The command displays the
returned `Pending`, `Ready`, or `Failed` state. A `Failed` result is printed and
then exits with status 1 so scripts do not mistake a saved but uninitialized
pool for a usable pool.

List dynamic desired pools or filter them to one resource manager:

```sh
det resource-pool list-dynamic
det resource-pool list-dynamic --cluster-name agent-cluster --json
```

After correcting the cause recorded on a failed operation, retry its saved
configuration with:

```sh
det resource-pool retry batch-a --cluster-name agent-cluster
```

Retry also exits with status 1 if initialization fails again. These commands
do not change the restart and rollback limits. Successful retry after a forced
runtime initialization failure remains covered by focused tests rather than the
normal live CPU lifecycle smoke.
