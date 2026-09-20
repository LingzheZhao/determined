# Fork maintenance validation record

Date: 2026-09-20. Source baseline: `c1e9c6d7b`.

This record separates evidence for the maintenance security changes from later
combined-branch experiments. A historical pass applies only to the referenced
revision and workflow definition.

## Local maintenance checks

In the split maintenance worktree, `tools/fork/check.sh quick` passed its one
test in 0.27 seconds. The security check passed **27 tests** on Python 3.12.11
across the common extractor, task-context preparation, and proxied checkpoint
download. The suite includes the compatibility path used when Python lacks tar
extraction filters. An independently built wheel was installed into an isolated
directory; the same 27 tests passed with import paths confirming that the
installed wheel was used. Black and isort passed for the changed Python files.

Go formatting and `git diff --check` passed. The focused authorization test did
not complete because its required modules were absent from the offline local
cache, so no local Go test pass is claimed for that attempt. PostgreSQL and
real-container checks require an appropriate local environment and remain
explicit rather than automatic.

## Accepted security baseline CI

Commit `1131962fac093120f6ed1c28670fcc285a6602d5` passed the
[initial fork baseline run](https://github.com/LingzheZhao/determined/actions/runs/35467384948).
Its downloaded wheel was versioned `0.38.1+fork.1131962fac09` and contained the
safe extractor.

Commit `ffa1dd9dc53e5df4defce10fa89393daa22ca28a` passed the
[final fork baseline run](https://github.com/LingzheZhao/determined/actions/runs/35467726024).
That run completed:

- master and agent Linux builds with an explicit fork version and checksums;
- task-control authorization tests and existing Go archive tests;
- eight selected PostgreSQL-backed authorization/traversal tests under the race
  detector, including descendant denial and exact-snapshot mutation;
- a harness wheel build and Python 3.8/3.12 archive suites that verified imports
  came from the downloaded wheel.

These runs are evidence for the exact tested revisions. The retained maintenance
workflow is now manual and contains build/package steps rather than the historical
test matrix.

## Combined-branch distribution evidence

Commit `c11dcbcec9e9` passed
[distribution run 35504119197](https://github.com/LingzheZhao/determined/actions/runs/35504119197)
on the earlier combined maintenance and pool-development branch. It built the
wheel, locked front end, HTML docs, Linux binaries, master and agent images,
manifest, checksums, and candidate artifact, and its disposable CPU cluster ran
real work successfully. The artifact was named
`determined-fork-distribution-0.38.1-fork.c11dcbcec9e9-linux-amd64`.

That run does **not** establish that this split maintenance change or a future
commit has passed CI. It also must not be used as acceptance evidence for the M1
roadmap in this maintenance-only change. Run the manual candidate build when a
reviewer needs an artifact for the exact split revision.

## Remaining release gates

- Build a manual candidate for the exact revision selected for promotion.
- Run an existing research workload on the intended agent/Docker environment.
- Rehearse PostgreSQL restore and artifact rollback.
- Record supported runtime versions and the registry/package destination before
  publishing anything.

Successful checks do not constitute a production deployment, GPU validation, or
a published fork release.
