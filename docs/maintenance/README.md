# Research-cluster fork maintenance

This fork retains Determined's experiment management and scheduling while working
toward online maintenance, heterogeneous hardware support, and less intrusive
training environments. The initial supported development focus is the static
agent/Docker path. Existing backends must continue to compile; extending every
cloud, framework, or enterprise integration is outside the initial scope.

The starting point is upstream commit `c1e9c6d7b` (the 0.38.1 release-note commit).
The project owner's supplied architecture review informed this plan. Historical
claims about upstream funding or support are not release guarantees for this fork.

## Delivery order and acceptance gates

| Milestone | Deliverable | Required evidence |
| --- | --- | --- |
| M0: maintainable baseline | Task-control authorization, safe archive extraction, fork CI, independent build artifacts | Negative security regressions; master/agent build; Python wheel, UI and docs artifacts; a real workload regression before release |
| M1: online resource pools | Append-only static agent pools backed by durable desired configuration and one registry | Create, observe readiness, join agent, submit work, restart master; existing pool objects, queues and allocations remain intact |
| M2: task continuity | Bounded telemetry buffering, allocation reconciliation and version compatibility | Short/long master outage, agent restart, task completion during outage; process progress and no duplicate GPU allocation |
| M3: research jobs | Hardware constraints, launch preflight, isolated script environment | Explain unsatisfied constraints; verify GPU/CPU/memory/mount requirements and unmodified training environments |
| M4: batch experiments | Idempotent submission and explicit checkpoint dependencies | Repeated submissions do not duplicate jobs; retries retain attempt history; evaluation pins immutable artifacts |

M0 candidate distribution builds and the M1 CPU-agent lifecycle have passed
integration acceptance. An isolated maintenance-to-pools upgrade and database
backup/restore rollback also passed on the workstation. GPU research workloads and production rollout
remain release gates. M2 has an opt-in CPU diagnostic baseline and nonblocking optional progress reporting.
Reliable metrics/checkpoint recovery and M3–M4 remain planned.
The [distribution guide](distribution.md) describes candidate artifacts; the
[dynamic pool guide](dynamic-pools.md) documents the implemented management API.
The [online pool design](online-resource-pools.md) retains the full acceptance matrix.
Record actual checks and remaining gates in [validation.md](validation.md).

## Baseline behavior changes

Generic Task kill, pause, and unpause now require control authorization. With basic
authorization, only the task owner or an administrator can control a task; tasks
without an owner require an administrator. RBAC uses the existing workspace
`UPDATE_NSC` permission in addition to visibility. Cascades check every affected
task before any state update or allocation action. Other notebook/shell/command
authorization policies are unchanged.

Task-context and proxied checkpoint archives reject escaping paths/links and
special files. Archive ownership and unsafe permission bits are not restored.
Contained symbolic links and hard links remain supported, but hard links must
resolve to an existing regular file in the extraction directory; unsafe tar link
fallbacks are rejected. Proxied checkpoint validation uses a temporary archive
file, so the destination filesystem needs space for both the downloaded archive
and extracted contents. This change does not impose an archive size quota or make
extraction transactional.

## Working model

- The project lead owns scope, architecture, integration review, and acceptance.
  Keep an explicit next milestone and pick bounded work from its checklist.
- GPT-6-sol subagents implement fixes, tests, build plumbing, and documentation
  in explicitly assigned, non-overlapping files. Each returns the changed paths,
  exact validation performed, and unresolved risks. Review their changes before
  promoting a milestone.
- GPT-6-luna handles read-only status checks: workflow results, tool availability,
  missing artifacts, and progress against the checklist. Escalate new failures to
  the lead; do not silently expand monitoring into scheduler or security changes.
- Run focused regression checks locally through `tools/fork/check.sh`. The default
  is deliberately small; database/race and real-container acceptance are explicit
  options. Do not make every edit rebuild or retest the whole project.
- GitHub Actions is reserved for manually requested candidate builds. It does not
  run tests, lint, or smoke suites on pushes or pull requests. Reuse existing
  successful evidence when the relevant code has not changed. Keep the last
  accepted artifacts and distinguish local checks from historical CI evidence.
- Keep security, persistence, and compatibility regressions that protect shipped
  behavior. Remove disposable development probes and redundant tests when a feature
  settles; do not retain a growing test suite merely because it was written.

Use a `codex/` development branch and focused pull requests. Do not rewrite active
work from another contributor. Changes to API schemas include regenerated bindings;
database changes include reversible migrations and restart tests. Changes affecting
running jobs must state their effects on task identity, ownership, and reservations.

## Current work queue

1. Completed in the baseline branch: targeted task-control and archive security
   fixes, public-path regressions, and a successful fork baseline workflow. See
   [validation.md](validation.md) for the tested revision and CI evidence.
2. Completed candidate acceptance: binaries, wheel, UI, HTML docs, loadable
   master/agent images, manifest, and checksums. Subsequent candidate builds are
   manual; routine development validation stays local.
3. Completed CPU lifecycle acceptance: create/replay, authorization, unchanged
   running allocation and Docker container, new-pool work, and restart recovery.
   Failed initialization and concurrency have focused database/unit coverage.
   Exercise the intended GPU research environment separately.
4. Run an existing research workload on a disposable agent/Docker cluster; retain
   configuration, image digest, checkpoint, task identity, and before/after results.
5. Measure M2 control-plane outages with the opt-in
   [CPU continuity probe](task-continuity.md) while preparing GPU regression.
   Separate process survival, continued computation, and recovered metadata; do
   not infer long-running GPU continuity from the CPU lifecycle smoke.

## Release and compatibility policy

Preserve Apache 2.0 notices and attribution. Keep historical upstream documentation
available, but distinguish it from verified fork behavior. Report issues for this
fork in `LingzheZhao/determined`; the upstream contribution and CLA process below
the README fork notice describes upstream contributions.

Use Go 1.22 as the reproduction baseline specified by this source tree, not as a
claim that it remains a supported security toolchain. Upgrade dependencies in
separate measured changes. Likewise, exercising Python 3.8 is a compatibility
check for this tree, not an endorsement of deploying an obsolete interpreter.

Before the first fork release, select and record the fork version scheme, package
and container destinations, supported production runtime versions, and rollback
procedure. Produce checksums and a manifest identifying the source revision and
dependencies. Include master and agent binaries/images, the Python wheel, front-end
static assets, and necessary deployment documentation. CI artifacts are candidates,
not a published release. The old publishing workflows and defaults require separate
review before use in this fork.

For M2, classify traffic by meaning: lossy observations may have bounded discard
policies; searcher decisions, checkpoint commits, and task transitions require
reliable, idempotent handling. A missing connection never proves resources are free.
GPU sharing, dual-active masters, framework-wide modernization, and front-end
rewrites are separate proposals rather than implicit parts of these milestones.
