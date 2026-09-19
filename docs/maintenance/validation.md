# Baseline validation record

Date: 2026-09-20. Source baseline: `c1e9c6d7b`.
Development branch: `codex/fork-maintenance-baseline`.

This record separates local evidence from the checks required before releasing M0.

## Environment

The local host is macOS arm64. Python 3.12/3.13 and Node 25 are installed;
these differ from some historical build requirements. Docker's client is present,
but the daemon was unavailable during the initial check. PostgreSQL-backed tests
and real agent/GPU fault injection therefore need an additional test environment.

A Go 1.22.12 toolchain was downloaded to a temporary directory and its archive
checked against the official Go release checksum. It does not alter the system Go
installation. Go 1.22 reproduces this repository's build baseline; a supported
security toolchain upgrade remains separate work.

## Local checks

The focused archive suite passed on Python 3.12.11: **27 tests passed**. It covers
the common extractor, task-context preparation, and proxied checkpoint download:

```sh
python -m pytest -q harness/tests/common/test_tarfile_utils.py \
  harness/tests/exec/test_prep_container.py \
  harness/tests/checkpoints/test_checkpoint.py
```

The suite includes a forced no-filter compatibility path. This does not replace
running on an actual Python 3.8 interpreter; the workflow includes 3.8 and 3.12 jobs.
The warnings observed were deprecations in tarfile's compatibility branch and the
HTTP response test library.

Black 23.3 and isort 5.11.5 checks passed for the six changed Python files. The
repository's old Flake8 3.9.2 stack is incompatible with the local Python 3.12
runtime; a reduced compatibility check passed, but a full repository lint pass is
not claimed.

The harness wheel also built locally with `VERSION=0.38.1+fork.local` and
`pip wheel --no-deps --no-build-isolation ./harness`. Its contents include the new
extractor and both updated download modules. This temporary local wheel is a
packaging check, not a release artifact or a full independent distribution.

Go formatting and `git diff --check` passed. The focused local Go authorization
test did **not** complete: the cold dependency download was stopped after about
six minutes, and an offline retry confirmed missing modules. No local Go compile
or test pass is claimed. PostgreSQL-backed tests also require generated mocks and
a running database; the fork workflow provides both.

Actionlint 1.7.7 passed for the three changed workflows. YAML parsing passed as
well, and maintenance-document links resolve locally. A successful remote CI run
has not yet been recorded.

## Remaining release gates

- A successful remote run of the new fork workflow, attached to an exact commit.
- PostgreSQL-backed authorization regressions, including forbidden tree cascades.
- Supported Python compatibility checks for extraction and packaged call sites.
- Complete independent artifact set: master/agent images, Python wheel, front-end
  static assets, deployment documentation, checksums and source manifest.
- Real research workload regression and a rehearsed rollback on agent/Docker.
- Fork version, registry/package destinations, and production compatibility matrix.

The online pool lifecycle and control-plane fault-injection matrices belong to M1
and M2. Neither is validated by this baseline. No GitHub Actions run, production
deployment, or published fork release is implied by adding a workflow file.
