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

M0 is in progress. M1–M4 describe planned behavior, not capabilities shipped by this
branch. No production upgrade should be inferred from passing unit tests alone.
The [online pool design](online-resource-pools.md) splits M1 into reviewable changes.
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
- GPT-5.6-sol subagents implement fixes, tests, build plumbing, and documentation
  in explicitly assigned, non-overlapping files. Each returns the changed paths,
  exact validation performed, and unresolved risks. Review their changes before
  promoting a milestone.
- GPT-5.6-luna handles read-only status checks: workflow results, tool availability,
  missing artifacts, and progress against the checklist. Escalate new failures to
  the lead; do not silently expand monitoring into scheduler or security changes.
- GitHub Actions executes repeatable checks. A workflow definition is not evidence
  of a successful run. Keep logs/artifacts, distinguish skipped from passed, and
  avoid upstream-only credentials in the fork baseline.

Use a `codex/` development branch and focused pull requests. Do not rewrite active
work from another contributor. Changes to API schemas include regenerated bindings;
database changes include reversible migrations and restart tests. Changes affecting
running jobs must state their effects on task identity, ownership, and reservations.

## Current work queue

1. Close the two targeted security regressions and validate their public call paths.
2. Run the fork baseline workflow and resolve real failures; record results here.
3. Produce the complete M0 artifact set without upstream private services. Use an
   explicit fork version and image destination; do not reuse upstream publish jobs
   with their default organization or claim that the upstream PyPI package is this fork.
4. Run an existing research workload on a disposable agent/Docker cluster; retain
   configuration, image digest, checkpoint, task identity, and before/after results.
5. Start M1 with a registry refactor that preserves behavior, followed by durable
   configuration and startup recovery, then the create/status API and end-to-end tests.

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
