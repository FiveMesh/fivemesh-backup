# Contributing

Thanks for helping improve FiveMesh Backup. Contributions should stay small,
testable, and focused on reliable local backup workflows.

## Development setup

You need Go (the version in `go.mod`) and, for manual testing, the MariaDB or
MySQL client tools.

```sh
git clone https://github.com/FiveMesh/fivemesh-backup.git
cd fivemesh-backup
make check        # go vet + go test
make build        # bin/fivemesh-backup
```

Use a separate configuration home while experimenting so your real profiles and
scheduled tasks are never touched:

```sh
FIVEMESH_BACKUP_HOME=/tmp/fmb-dev ./bin/fivemesh-backup setup
```

```powershell
$env:FIVEMESH_BACKUP_HOME = "$env:TEMP\fmb-dev"; .\bin\fivemesh-backup.exe setup
```

`docs/testing.md` explains how to run a disposable MariaDB for end-to-end tests.

## Before opening a pull request

1. Never test against production databases.
2. Keep local backups independent of any network service.
3. Add dependencies only when the standard library is clearly insufficient.
4. Keep credentials out of command arguments, logs, fixtures, and commits.
5. Run `gofmt -l .`, `go vet ./...`, and `go test ./...` (CI runs them on Linux
   and Windows, plus `go test -race` on Linux).
6. Run the installer regression for the platform you changed:

```sh
bash install/test-install.sh
```

```powershell
.\install\test-install.ps1
```

Changes to restore behavior, archive format, credential storage, retention, or
scheduling need tests and documentation updates.

## Interactive prompts

Setup renders rich prompts on a terminal and plain numbered prompts when input
is redirected. Tests drive the plain mode through `wizard.NewPrompter` with a
`strings.Reader`; set `FIVEMESH_BACKUP_PLAIN=1` to see the same mode by hand.

## Releases and docs

Release artifacts are described in [docs/release.md](docs/release.md).
Documentation builds with MkDocs Material:

```sh
mkdocs build --strict
```
