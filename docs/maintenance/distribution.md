# Fork distribution candidates

The `Fork distribution` GitHub Actions workflow is a manually dispatched build.
It has no push, pull-request, schedule, release, or registry-publication trigger.
The operator supplies an optional fork version, a local image namespace, and an
Ubuntu-compatible base image or digest.

## Build contents

The Linux amd64 build uses Go 1.22.12, Node 20.19.5 with the committed npm lock,
Python 3.10, Helm 3.15.2, and protoc 25.3. Its candidate artifact contains:

- executable master, agent, and `determined-gotmpl` binaries;
- the Python wheel, locked front-end assets, and generated HTML documentation;
- gzip-compressed Docker archives for the master and agent;
- `MANIFEST.json`, `SHA256SUMS`, and deployment documentation.

The manifest records the source commit, fork version, platform, tool versions,
dependency-file hashes, base image reference, image names, and image IDs. The
workflow does not build or claim a GPU image.

## Verify and load

Download and extract the candidate on a Linux amd64 host, then verify it before
loading its images:

```sh
sha256sum --check SHA256SUMS
gzip -dc determined-fork-*-master-image.tar.gz | docker load
gzip -dc determined-fork-*-agent-image.tar.gz | docker load
```

Use the exact image names from `MANIFEST.json`. Loading the archives only changes
the local Docker image store. The master requires PostgreSQL plus writable cache
and checkpoint locations. A static Docker agent normally needs network access to
the master and `/var/run/docker.sock`; that socket grants control of the Docker
host and belongs only on trusted machines.

## Optional local CPU smoke

The workflow does not run a cluster smoke. To test the delivered wheel and images
locally, use a matching source checkout and build the disposable CPU task fixture
from the delivered wheel:

```sh
artifact_dir=/absolute/path/to/extracted-candidate
fork_version=$(jq -r .version "${artifact_dir}/MANIFEST.json")
fork_commit=$(jq -r .source.commit "${artifact_dir}/MANIFEST.json")
fork_tag=${fork_version//+/-}
git switch --detach "${fork_commit}"
python -m pip install "${artifact_dir}"/determined-*.whl
mkdir -p .fork-build/master/wheels
cp "${artifact_dir}"/determined-*.whl .fork-build/master/wheels/
docker build -f tools/fork/Dockerfile.smoke-task \
  --build-arg "FORK_COMMIT=${fork_commit}" \
  --build-arg "FORK_VERSION=${fork_version}" \
  -t "local/determined-fork/smoke-task:${fork_tag}" .
export FORK_MASTER_IMAGE=$(jq -r .images.master.name "${artifact_dir}/MANIFEST.json")
export FORK_AGENT_IMAGE=$(jq -r .images.agent.name "${artifact_dir}/MANIFEST.json")
export FORK_TASK_IMAGE="local/determined-fork/smoke-task:${fork_tag}"
tools/fork/smoke.sh
```

The smoke starts disposable PostgreSQL and master containers, verifies password
login through the delivered CLI, joins one static CPU agent in the default pool,
and runs a short command. It makes no GPU or online-pool claim.

## Promotion and rollback

Candidates are retained for 14 days. Promote one only after local checks, a real
research-workload regression, and a compatible PostgreSQL backup/restore rehearsal
pass for the same source revision. Roll back by stopping agents, restoring the
compatible database backup, loading the previous image archives, and starting the
previous master before its agents. Selecting an older image does not reverse a
database migration.
