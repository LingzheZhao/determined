# Fork maintenance validation record

Date: 2026-09-20. Source baseline: `c1e9c6d7b`.
Development branch: `codex/fork-maintenance-baseline`.

This record separates local evidence from the checks required before releasing M0
and accepting the first M1 online-pool lifecycle.

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
extractor and both updated download modules. Installing that wheel into an isolated
directory and running the same suite with `--import-mode=importlib` also passed all
27 tests; the module paths confirmed that the installed wheel was exercised. The
workflow uses this import mode and checks the import origin explicitly. This
temporary local wheel is a packaging check, not a release artifact or a full
independent distribution.

Go formatting and `git diff --check` passed. The focused local Go authorization
test did **not** complete: the cold dependency download was stopped after about
six minutes, and an offline retry confirmed missing modules. No local Go compile
or test pass is claimed. PostgreSQL-backed tests also require generated mocks and
a running database; the fork workflow provides both.

Actionlint 1.7.7 passed for the three changed workflows. YAML parsing passed as
well, and maintenance-document links resolve locally.

## Original baseline CI

The final implementation/workflow commit is
`ffa1dd9dc53e5df4defce10fa89393daa22ca28a`. Its
[Fork baseline run](https://github.com/LingzheZhao/determined/actions/runs/35467726024)
completed successfully:

- Master and agent Linux builds, with an explicit fork version and checksums.
- Basic task-control policy unit tests and existing Go archive tests.
- Eight selected PostgreSQL-backed task authorization/traversal tests with the
  race detector, including root/descendant denial and exact-snapshot mutation.
- Harness wheel build and candidate artifact upload.
- Python 3.8 and 3.12 archive suites, including an assertion that the tested module
  comes from the downloaded wheel and `--import-mode=importlib` to avoid source
  checkout shadowing.

The [initial implementation run](https://github.com/LingzheZhao/determined/actions/runs/35467384948)
for `1131962fac093120f6ed1c28670fcc285a6602d5` also passed. Its downloaded wheel
metadata was checked locally: version `0.38.1+fork.1131962fac09`, with the safe
extractor included. Candidate artifacts are retained by these workflows for seven
days. The binaries are uploaded inside a tarball to retain executable permissions.

Subsequent documentation-only commits may use `[skip ci]`; the exact tested code
revision and successful run above remain the evidence, rather than implying that
an untested code change passed.

## Online-pool local validation

The installed-wheel check was repeated after adding the pool CLI. The wheel was
built with `VERSION=0.38.1+fork.poolcheck` and installed into a separate temporary
directory. Explicit import checks confirmed both `determined` and
`determined.cli.resource_pool` came from that directory. With the installed wheel
first on `PYTHONPATH` and the harness test helpers second, the combined archive
and CLI suite passed **33 tests** on Python 3.12.11. The CLI tests exercise actual
argument parsing and mocked HTTP requests, including YAML/JSON loading, cluster
selection, required idempotency keys, status output, and a nonzero exit on Failed.

Black and isort checks passed for the two CLI files. Actionlint passed for both
fork workflows. The local Docker daemon remains unavailable; image builds and
agent lifecycle checks initially ran in a disposable GitHub Actions environment. Local
Go dependency downloads were stopped without a completed local Go test result;
remote compile, race, and PostgreSQL results are recorded separately.

## Online-pool baseline CI

Commit `c495ec290bc0443239de58f61d8eeac03563831f` passed the
[Fork baseline workflow](https://github.com/LingzheZhao/determined/actions/runs/35501677285).
It built master, agent, and the wheel and passed:

- Race-enabled registry publication/copy isolation, live scheduler defaults, and
  frozen dynamic task defaults tests.
- Strict input/persisted-config validation and real basic-authorization middleware
  tests, including inactive administrators and non-admin denials.
- A forced Ready-state write failure that stops the prepared runtime and leaves
  the pool unpublished.
- PostgreSQL idempotency, concurrent creation, restart recovery, schema-version
  rejection, and YAML/database name-collision tests.
- Existing agent routing/queue tests and the prior generic-task authorization suite.
- All 33 installed-wheel archive/CLI tests on both Python 3.8 and 3.12.

These results validate the implementation and database paths. The independently
built images and real CPU-agent lifecycle have their own acceptance run; the
baseline workflow alone does not prove that lifecycle or GPU task continuity.

The same baseline suite passed again at `e4149a7f3` in
[run 35503036589](https://github.com/LingzheZhao/determined/actions/runs/35503036589)
after the fork lint and test-fixture corrections.

## Accepted CPU lifecycle and candidate distribution

Commit `c11dcbcec9e9` passed the complete
[distribution run 35504119197](https://github.com/LingzheZhao/determined/actions/runs/35504119197).
The run built the wheel, locked front end, HTML docs, Linux binaries, master/agent
images, and a disposable CPU task image. Its live cluster accepted:

- Anonymous and non-administrator denials for pool management.
- A real static CPU task, followed by online pool creation while another task ran.
- The same task, allocation, Determined container, and actual Docker container
  before and after creation, with running-state checks and continued task output.
- Idempotent creation returning HTTP 200 without a second pool and a successful
  query through the installed wheel's `resource-pool list-dynamic` command.
- New-pool agent admission and work, then master restart, actual enabled-agent
  reconnection in both pools, durable-pool recovery, and new work after recovery.

Packaging, file checksums, executable permissions, and artifact upload also passed.
The retained candidate is
`determined-fork-distribution-0.38.1-fork.c11dcbcec9e9-linux-amd64`
(artifact ID `10603607257`, 350,959,705 bytes, 14-day retention). GitHub's archive
digest is `sha256:4693298752029c1660a42cfed0090112e54d73ef005e6e5af4aa531dce133163`;
the artifact also includes its own `MANIFEST.json` and `SHA256SUMS`.

The same source passed the
[baseline](https://github.com/LingzheZhao/determined/actions/runs/35504121883),
[Go lint](https://github.com/LingzheZhao/determined/actions/runs/35504121844), and
[Python checks including full mypy](https://github.com/LingzheZhao/determined/actions/runs/35504121833).
Bindings, documentation, and pre-commit checks passed too.

## Development validation and Actions budget

After that acceptance, the project owner requested local, small test runs and
separate maintenance and durable-pool PRs. The retained Actions definition is a
manual candidate build only; it does not run tests, lint, or cluster smoke, and it
has no push or PR trigger. `tools/fork/check.sh` provides focused local checks;
`tools/fork/smoke.sh` remains an explicit local container acceptance tool.

The historical successful runs above remain evidence for the tested code. Moving
the checks locally does not imply a new run occurred. Keep behavioral regressions,
remove disposable development probes when no longer useful, and select checks
according to the changed behavior instead of running every suite for every edit.

The old upstream administrative `pull_request_target` definitions still come from
`main` until the maintenance PR is integrated. Their old failures are distinct from
product acceptance. Test/lint workflows were explicitly disabled remotely to stop
further automatic test spending during the transition.

## Remaining release gates

- Real research workload regression and a rehearsed rollback on agent/Docker.
- Fork version, registry/package destinations, and production compatibility matrix.

The broader concurrency, queue-ordering, crash-point, and GPU matrices remain
separate from the accepted normal CPU lifecycle. Successful checks do not constitute
a production deployment or a published fork release.
