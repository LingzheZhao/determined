# Local fork checks

Use `tools/fork/check.sh` during development instead of running the repository's full test
matrix. The script uses tools already installed on the workstation. It does not install or
upgrade packages, build release binaries, start containers, or trigger GitHub Actions.

The default check is intentionally small:

```bash
tools/fork/check.sh
```

It runs one checkpoint archive-safety regression and, when the dynamic resource-pool CLI is
present, its six CLI tests. It disables pytest's cache and Python bytecode output so the run does
not leave test artifacts in the checkout. On a maintenance-only branch without the pool CLI, the
archive regression still runs.

Run additional tests only for the area being changed:

```bash
# Run the complete focused Python archive-safety suite.
tools/fork/check.sh security

# Run registry, scheduler, and dynamic-pool unit tests with Go's race detector.
tools/fork/check.sh pools

# Run dynamic-pool persistence and restart tests against an existing test database.
DET_INTEGRATION_POSTGRES_URL='postgres://postgres:postgres@localhost:5432/determined?sslmode=disable' \
  tools/fork/check.sh integration-pools
```

`pools` and `integration-pools` require the dynamic-pool source files, so they fail with a clear
message on a maintenance-only checkout. The integration mode never starts PostgreSQL or Docker;
the database URL must point to a disposable database that is already running.

The Go modes expect the repository's generated mocks and development dependencies to be ready.
Generate mocks with an existing compatible `mockery` installation when the checkout does not
already contain them:

```bash
make -C master mocks
```

The script does not install Python packages or Go tools. A Go command may still download modules
on a cold module cache according to the selected toolchain's normal `GOPROXY` settings.

For a maintenance-only authorization or archive change, these smaller Go checks can be run
directly without enabling the dynamic-pool modes:

```bash
go test ./master/internal/command -run '^TestCanControlGenericTaskBasic$' -count=1
go test ./master/pkg/archive ./master/pkg/checkpoints/archive
```

Set `PYTHON` or `GO` to select an existing toolchain. For example:

```bash
PYTHON=/path/to/venv/bin/python tools/fork/check.sh quick
GO=/path/to/go/bin/go tools/fork/check.sh pools
```

Missing interpreters, Python packages, race support, source files, or database settings are
reported as errors. The script never silently skips an applicable test. Use the repository's
existing checks separately when broader formatting or lint coverage is needed:

```bash
make -C harness check
make -C master check
```

For an explicit, disposable CPU control-plane fault probe, see
[task continuity](../../docs/maintenance/task-continuity.md). It measures a real
NumPy/Core API experiment and is never part of the default quick check.
