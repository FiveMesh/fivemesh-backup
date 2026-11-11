# Release and artifact preparation

Releases are built by `.github/workflows/release.yml` when a `v*` tag is
pushed. The workflow runs the full test suite, builds every artifact with
version metadata, writes `SHA256SUMS`, and creates a draft GitHub release for a
maintainer to review and publish.

## Versioning

FiveMesh Backup follows [Semantic Versioning](https://semver.org). Within 1.x:

- `config.toml` and `credentials.toml` written by any 1.x release keep
  loading in later 1.x releases.
- Archives (`.sql.zst` plus metadata) stay readable by `verify`, `list`, and
  `restore`.
- Commands, flags, and exit codes are not removed or changed in meaning;
  new ones may be added.
- Scheduled tasks and timers created by 1.x keep working after an upgrade,
  or `schedule status` says exactly which command updates them.

Anything that breaks one of these needs a 2.0. Bug fixes are patch releases
(1.0.1); new features are minor releases (1.1.0).

## Artifact structure

Use one directory per release version:

```text
v1.0.0/
  fivemesh-backup-linux-amd64
  fivemesh-backup-linux-arm64
  fivemesh-backup-windows-amd64.exe
  SHA256SUMS
  SHA256SUMS.sig
```

Artifact names are stable so installers can select by operating system and CPU
architecture. Linux binaries are extensionless executables; Windows uses the
`.exe` suffix.

## Build inputs

Set version metadata explicitly rather than relying on `-dev` defaults:

```sh
VERSION=1.0.0
COMMIT="$(git rev-parse --short HEAD)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

make release-artifacts VERSION="$VERSION" COMMIT="$COMMIT" BUILT_AT="$BUILT_AT"
```

The release process should use a clean tagged commit, reproducible build flags,
and the same Go toolchain declared in `go.mod`.

## Integrity and authenticity

- Every artifact must be listed in `SHA256SUMS`.
- The checksum manifest must be signed before public distribution.
- Windows binaries should additionally receive Authenticode signing before
  general distribution.
- Installer scripts must verify checksums before replacing an executable.
- Future automatic updates require a separately reviewed design.

Checksums detect corruption and unintended substitution only when obtained
through an authenticated channel. They are not a substitute for signatures.

## Publishing a release

1. Merge to `main` and make sure CI is green.
2. Tag and push: `git tag v1.0.0 && git push origin v1.0.0`. Tags with a
   suffix such as `v1.0.0-rc.1` become pre-releases, which the installers'
   `latest` channel ignores.
3. The workflow tests, builds the binaries, copies `install.sh` and
   `install.ps1`, writes `SHA256SUMS`, attests build provenance (public
   repository only), and opens a **draft** release.
4. Review the draft notes and assets, then publish. Publishing is what makes
   `releases/latest/download/...` and the one-line installers pick it up.

Planned improvements:

- Authenticode signing for the Windows binary (for example Azure Trusted
  Signing) to avoid SmartScreen warnings.
- winget and Scoop manifests.

## Upgrade compatibility

Both installers replace a stable per-user executable path. Native schedules
reference that path, so normal in-place upgrades do not require schedule changes.

If an installation moves to a custom path:

- Run `configure` or `schedule configure` so schedule actions use the new path.
- Preserve configuration, credentials, history, and archives.
- Do not delete the previous executable until schedules have been verified.
