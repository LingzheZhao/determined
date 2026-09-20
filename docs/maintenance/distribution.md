# Fork distribution candidates

The `Fork distribution` GitHub Actions workflow builds a self-contained candidate
set for this research-cluster fork. It does not publish to a container registry or
package index. Each run records the source commit, fork version, tool versions,
local image names and image IDs in `MANIFEST.json`; `SHA256SUMS` covers every
delivered file. Treat a successful workflow artifact as a release candidate, not
as a supported production release.

## Supported build baseline

The workflow uses Linux amd64, Go 1.22.12, Node 20.19.5 with the committed npm
lockfile, Python 3.10, Helm 3.15.2, and protoc 25.3. The default candidate version
is `0.38.1+fork.<12-character commit>`. A manual run can provide another
PEP 440-compatible fork version, a local image repository, and a base image. The
image tag replaces `+` with `-`. These versions reproduce the source tree's build
contract; they are not a promise of security support beyond this candidate.

The artifact contains executable master, agent, and `determined-gotmpl` binaries;
the Python wheel; front-end assets; generated HTML documentation; and gzip-compressed
Docker archives for the master and agent. The master image embeds the wheel,
generated API description, front end, docs, migrations, and runtime scripts needed
by agent tasks.

## Verify and load

Download and extract the workflow artifact on a Linux amd64 host, then verify it
before loading images:

```sh
sha256sum --check SHA256SUMS
gzip -dc determined-fork-*-master-image.tar.gz | docker load
gzip -dc determined-fork-*-agent-image.tar.gz | docker load
```

Use the exact image names from `MANIFEST.json`. The default names are under
`local/determined-fork`, deliberately separate from upstream registry namespaces.
Loading an archive only changes the local Docker image store.

The master needs PostgreSQL and writable cache/checkpoint locations. The agent
needs network access to the master and access to a supported container runtime.
Static Docker agents normally mount `/var/run/docker.sock`; granting that mount is
equivalent to granting control of the Docker host, so restrict it to trusted
machines. Configure CPU agents with `slot_type: cpu`. This candidate makes no GPU
support claim.

For a disposable local example, copy `tools/fork/master-smoke.yaml` and
`tools/fork/docker-compose.smoke.yml` from the matching source revision, export
the two image names, and run the smoke script from the checkout:

```sh
export FORK_MASTER_IMAGE=local/determined-fork/master:<tag>
export FORK_AGENT_IMAGE=local/determined-fork/agent:<tag>
tools/fork/smoke.sh
```

The smoke creates an isolated PostgreSQL database, checks master health and admin
login, waits for a CPU agent to join, and runs a short command to completion. A
manual workflow run can additionally enable the dynamic-pool extension; it creates
a pool through the authenticated API while an original-pool task is running,
verifies that task keeps its identity and advances, joins a second CPU agent, runs
work in the new pool, restarts the master, verifies recovery, and runs new work in
the recovered pool. It does not test a GPU path.

## Rollback and retention

Keep the previous verified artifact and its `MANIFEST.json`. Roll back by stopping
agents, restoring the compatible PostgreSQL backup taken before the upgrade, loading
the previous image archives, and restarting the previous master before its agents.
Database migrations are not reversed merely by selecting an older image; validate
backup restore and task/checkpoint visibility in a disposable environment first.

GitHub retains candidates for 14 days. Promote artifacts only after the real
research-workload gate and rollback rehearsal in the maintenance validation plan
have passed for the same source revision.
