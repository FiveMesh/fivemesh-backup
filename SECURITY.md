# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub:
**Security → Report a vulnerability** on
[FiveMesh/fivemesh-backup](https://github.com/FiveMesh/fivemesh-backup/security/advisories/new).

Do not open a public issue for security problems. Include the affected version
(`fivemesh-backup version`), operating system, reproduction steps, and impact.
Never include real database credentials, SQL data, or archive contents.

We aim to acknowledge reports within a few working days and will coordinate a
fix and disclosure with you.

## Supported versions

Security fixes go into the latest 1.x release. Please upgrade to the newest
version before reporting.

## Verifying downloads

Every release lists SHA-256 checksums in `SHA256SUMS`, and the installers
refuse a binary that does not match. Release artifacts also carry GitHub build
provenance, which you can check with:

```sh
gh attestation verify fivemesh-backup-linux-amd64 --repo FiveMesh/fivemesh-backup
```

## Design notes

Database passwords are stored in a separate credentials file protected by
mode `0600` on Linux and an explicit protected NTFS DACL on Windows. FiveMesh
Backup does not use an OS keychain or custom cryptography. See
[docs/security.md](docs/security.md) and
[docs/windows-security.md](docs/windows-security.md) for the threat model.
