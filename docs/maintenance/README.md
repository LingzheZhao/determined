# Research-cluster fork maintenance

This fork starts from upstream commit `c1e9c6d7b` and focuses first on a
maintainable agent/Docker deployment for research clusters. Existing backends
remain in the source tree, but this maintenance baseline does not claim new
support for every cloud, scheduler, framework, or GPU configuration.

## Maintained baseline

The first maintenance changes harden two existing behavior boundaries:

- Generic Task kill, pause, and unpause require control authorization. Under
  basic authorization, the task owner or an administrator may control the task;
  ownerless tasks require an administrator. RBAC uses the existing workspace
  permission. Cascades authorize every affected task before changing any state.
- Task-context and proxied-checkpoint tar archives reject paths, links, and
  special files that can escape the extraction directory. Extraction does not
  restore archive ownership or unsafe permission bits. Contained symbolic and
  hard links remain supported under the documented checks.

The [validation record](validation.md) distinguishes checks run for these changes
from later evidence produced on a combined development branch.

## Build and local checks

GitHub Actions retains one manually dispatched candidate build. It produces the
wheel, front end, HTML documentation, Linux binaries, master and agent image
archives, a manifest, and checksums. It does not publish to a registry or package
index and does not run automatically for pushes or pull requests. See the
[distribution guide](distribution.md).

Routine development checks run locally. Use the smallest check that covers the
changed behavior, preserve regressions that protect security boundaries, and run
the optional Docker smoke only for image startup or agent-path changes. The
repository-local commands are documented in `tools/fork/local-checks.md`.

## Roadmap

| Milestone | Status | Intended outcome |
| --- | --- | --- |
| M0: maintainable baseline | Current | Security fixes, reproducible candidate artifacts, and focused local validation |
| M1: online resource pools | Planned | Append-only pool creation with durable desired state, authorization, and restart recovery |
| M2: task continuity | Planned | Bounded telemetry buffering, allocation reconciliation, and explicit version compatibility |
| M3: research jobs | Planned | Hardware constraints, launch preflight, and isolated script environments |
| M4: batch experiments | Planned | Idempotent submission and explicit checkpoint dependencies |

Future milestones require separate design, implementation, and acceptance
evidence. This maintenance change does not expose or claim an online resource-pool
API, master-outage task continuity, or GPU support.

## Release policy

Preserve Apache 2.0 notices and historical upstream documentation. Treat workflow
artifacts as candidates rather than published releases. Before promotion, record
the source revision, version, image destination, supported runtime matrix, real
research-workload result, database backup and restore rehearsal, and rollback
procedure. Keep the previous accepted artifact until rollback has been exercised.
