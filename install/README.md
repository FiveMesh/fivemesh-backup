# Installers

`install.ps1` (Windows) and `install.sh` (Linux) are attached to every GitHub
release and listed in its `SHA256SUMS`. By default they download the latest
release from `https://github.com/FiveMesh/fivemesh-backup/releases`, verify the
binary against that release's `SHA256SUMS`, install it for the current user,
and start guided setup.

```powershell
irm https://github.com/FiveMesh/fivemesh-backup/releases/latest/download/install.ps1 | iex
```

```sh
curl -fsSL https://github.com/FiveMesh/fivemesh-backup/releases/latest/download/install.sh | bash
```

Options: `-Version`/`--version` pins a release (for example `1.0.0`),
`-Sha256`/`--sha256` supplies an expected checksum instead of reading
`SHA256SUMS`, `-NoSetup`/`--no-setup` skips the wizard, and
`-BaseUrl`/`--base-url` (or `FIVEMESH_BACKUP_RELEASES`) points at a mirror
with the same layout: `<base>/latest/download/<file>` and
`<base>/download/v<version>/<file>`. With `irm | iex`, pass options by
downloading the script first: `& ([scriptblock]::Create((irm <url>))) -NoSetup`.

Installing a local build requires an explicit checksum:

Linux local install:

```sh
go build -trimpath -o bin/fivemesh-backup ./cmd/fivemesh-backup
sha256="$(sha256sum bin/fivemesh-backup | awk '{print $1}')"
bash install/install.sh --binary bin/fivemesh-backup --sha256 "$sha256"
```

Windows local install from PowerShell:

```powershell
go build -trimpath -o .\bin\fivemesh-backup-windows-amd64.exe .\cmd\fivemesh-backup
$sha256 = (Get-FileHash .\bin\fivemesh-backup-windows-amd64.exe -Algorithm SHA256).Hash
.\install\install.ps1 `
  -BinaryPath .\bin\fivemesh-backup-windows-amd64.exe `
  -Sha256 $sha256
```

Both installers verify every binary before replacing anything, never need
elevation, and preserve user configuration and data. Windows defaults to
`%LOCALAPPDATA%\FiveMesh\bin\fivemesh-backup.exe`; replacing that stable path
means existing Task Scheduler definitions automatically launch the upgraded
binary.

`install.ps1` is self-contained and may be distributed without the repository's
other installer files.

The Windows installer adds that directory to the user PATH unless
`-NoPathUpdate` is supplied. `-Uninstall -RemoveBinary` removes the exact
installer-managed PATH entry while leaving every unrelated entry untouched.
Passing `-NoPathUpdate` to uninstall suppresses PATH cleanup as well, which is
useful for isolated test or nonstandard deployments.

Run installer regression tests:

```sh
bash install/test-install.sh
```

```powershell
.\install\test-install.ps1
```

The regression tests also exercise downloads through a `file://` mirror with
the GitHub Releases layout, including tampered and missing `SHA256SUMS`
entries.
