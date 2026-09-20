# Local maintenance checks

Run `tools/fork/check.sh` from the checkout for one fast archive-safety regression.
Use `tools/fork/check.sh security` after changes to task-context or checkpoint
extraction to run the focused Python safety suite. Set `PYTHON=/path/to/venv/bin/python`
to select a prepared interpreter with the repository dependencies, pytest, and responses.
The script does not install packages, start services, or trigger GitHub Actions.
Missing tests and required packages fail explicitly.

For task-control authorization changes, the smallest Go check is:

```bash
go test ./master/internal/command -run '^TestCanControlGenericTaskBasic$' -count=1
```

Go checks require the repository's generated mocks and module cache; prepare these
once using the existing development instructions. A cold Go cache may download modules.
Use `GOPROXY=off` when downloads are unwanted. PostgreSQL-backed authorization tests
are opt-in and require an existing disposable test database. Use the existing
formatters only for the files being edited instead of running every repository check.

Keep the security regressions that protect shipped behavior. Remove temporary
probes and redundant development checks when they no longer add useful coverage.
