# Append-only online resource pools

Status: the M1 append-only static agent-pool API is implemented. See
[Dynamic resource pools](dynamic-pools.md) for the actual REST contract,
authorization rules, restart behavior, and limits. The normal CPU lifecycle has
passed with real Docker containers; see [validation](validation.md). The full
matrix below also includes concurrency, queue ordering, and crash-point cases
that are not all established by that smoke or by the focused database tests.

## Implemented seams

The agent service and resource manager now share an append-only registry. Desired
configuration is available during agent restoration, while regular resource-pool
lookups and scheduling see only entries whose runtime pool has been published
Ready. Dynamic desired configuration is loaded from PostgreSQL before the agent
service is constructed. The narrow M1 REST API is implemented directly in Echo;
there is no protobuf API change.

## First-version contract

- An authorized administrator creates a **static agent pool** with a unique name.
  Provider-backed pools and other resource managers return a clear unsupported
  error. Existing YAML-defined pools retain their current semantics.
- Persist the normalized desired configuration and schema/version metadata before
  initialization. Dynamic pool records are authoritative; YAML must never silently
  overwrite them. A name collision between sources blocks startup with an actionable
  error rather than choosing one source implicitly.
- Expose `Pending`, `Ready`, and `Failed` initialization states and a sanitized error.
  Desired state can be listed by authorized operators before scheduling is possible;
  agent admission, task validation, defaults, and scheduler lookup use Ready entries.
- Retrying creation with the same idempotency key and normalized configuration returns
  the existing operation. Reusing a key for a different configuration conflicts;
  another request for an occupied name also conflicts. Retry of failed initialization
  is explicit and reuses the saved desired configuration.
- Preserve existing pool object identity, scheduling queues, and allocation ownership.
  No rename, delete, agent migration, change of default pools, or allocation migration
  is included. The first version keeps one agent in one pool.

## Registry and persistence

Introduce one registry shared by agent admission and the resource manager. A Ready
entry contains the resolved configuration, its version, and the existing runtime
pool reference. Publish a fully initialized entry atomically. Readers use a locked
lookup or an immutable snapshot; never mutate maps returned to callers. Configuration
copies must isolate nested scheduler and container-default pointers too.

Do not hold registry locks across database calls, scheduler startup, agent callbacks,
or shutdown. Serialize create/retry operations per pool and ensure losing concurrent
creators clean up any partially initialized runtime resources. Database uniqueness
constraints enforce names and idempotency independently of process locks. A failed
database write must not make a pool available.

Save the effective defaults with dynamic desired configuration, so a change to
master YAML defaults does not silently change a dynamic pool on restart. Retain the
configuration schema version for future migrations; reject unsupported versions.
Export desired configuration and report collisions for an explicit future apply flow.

Persisted `Ready` is not proof of a live pool after restart. Load/validate all desired
configurations, construct the shared registry, restore eligible agent state using
that configuration, initialize runtime pools, and only then admit new agents and
allocations. If a dynamic pool fails recovery, retain its desired state and keep its
agents' persisted identities/reservations for reconciliation. Do not run the existing
unknown-pool cleanup against those records. Resolve this ordering and failed-pool
resource reservation behavior in tests before exposing the API.

## Implementation history

1. **Registry only (implemented):** replace all agent RM pool/config lookups and iterations and
   agent registration lookups with a shared interface, including job statistics,
   task container defaults, validation, agent-update callbacks, and shutdown. Preserve
   startup behavior and prove existing pool identity/queues stay unchanged.
2. **Durable configuration (implemented):** add migration, validation, unique constraints, and
   effective-config round-trip tests. Add recovery ordering and failed initialization
   cleanup; cover crashes between each durable/runtime step.
3. **API (implemented for REST):** add create/status/retry routes and master-config
   permissions without a protobuf change. Provider configuration is rejected and
   registry credentials are redacted in status. Basic-auth non-admin users cannot
   list, create, or retry.
4. **Acceptance:** run the lifecycle below against PostgreSQL and real agents. Only
   after it passes describe online creation as supported in user documentation.

## Acceptance matrix

| Scenario | Required result |
| --- | --- |
| Existing active pool while another is created | Same runtime pool, queue order, allocation IDs, containers, and progress |
| Concurrent same-name creates | One durable pool and runtime object; deterministic conflict/idempotent response |
| Invalid config or forbidden caller | No saved configuration and no runtime side effect |
| Initialization fails after persistence | Failed state is visible; new agents/tasks denied; explicit retry can recover |
| Master crashes before/after Ready publication | One recovered object; desired configuration retained; no double scheduling |
| Agent reconnects before initialization finishes | Retryable response; existing container identities and reservations retained |
| YAML/database collision | Clear failure; neither source silently wins |
| Master restarts after a new pool ran work | Pool and config version restored; agent joins; existing/new tasks accounted for |
| Unsupported RM/provider or attempted mutation | Rejected without altering existing pools |

Run focused tests with Go's race detector in addition to lifecycle integration tests.
The acceptance evidence must include process/container identity and task progress;
an unchanged Running label is insufficient.
