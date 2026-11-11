# Roadmap

FiveMesh Backup is local-first. Every future feature keeps local backups
working without an account or network access.

## Next

- Signed release artifacts and a public install script.
- MariaDB/MySQL version matrix for clients, privileges, and option
  compatibility in CI.
- Disk-full and filesystem-disappearance integration tests.
- Restore preflight for database existence, account privileges, and definer
  policy.
- History/log compaction with configurable bounds.

## Later: additional storage targets

Optional copies of each verified archive to:

- FiveMesh storage
- Cloudflare R2 and other S3-compatible buckets

Design constraints:

- Uploads run only after a verified local backup and never trigger a second
  dump.
- The local copy is kept unless the user turns it off.
- Remote credentials are scoped to upload, separate from database
  credentials, and never leave the machine.
- A failed upload never turns a successful local backup into a failure.
