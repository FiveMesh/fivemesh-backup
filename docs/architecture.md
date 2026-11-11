# Architecture

FiveMesh Backup is a native Go command-line program. Its primary boundary is a
portable backup engine that does not know how backups were scheduled.

## Components

- `internal/cli`: command routing, flags, exit codes, and terminal interaction.
- `internal/config`: strict TOML configuration, credential separation, defaults,
  and validation.
- `internal/mysql`: discovery of native MariaDB/MySQL clients and construction
  of argument-vector commands.
- `internal/backup`: execution orchestration, per-profile locking, phases, and
  history.
- `internal/archive`: streaming Zstandard output, atomic publication, metadata,
  checksums, and verification.
- `internal/retention`: profile-scoped, conservative archive cleanup.
- `internal/history`: local JSONL execution records and run logs.
- `internal/wizard`: interactive profile setup and reconfiguration.
- `internal/schedule`: backend-neutral scheduler lifecycle contract.
- `internal/systemd`: user-unit generation, timer lifecycle, and command adapter.
- `internal/taskscheduler`: native Windows Task Scheduler COM adapter.
- `internal/winsec`: native Windows DACL application and verification.

Scheduling calls the same short-lived `backup` command exposed by the CLI.
The systemd adapter writes independent service/timer pairs for each profile; it
does not contain backup logic. The Task Scheduler adapter registers one
independent least-privilege interactive-token task per profile and uses the same
transaction boundary.

## Backup flow

1. Load and strictly validate the selected profile.
2. Acquire a non-blocking advisory lock scoped to the profile.
3. Resolve the native dump client and record its version.
4. Wait up to 120 seconds for an authenticated native-client connection to the
   selected database. Only recognized availability errors are retried.
5. Create a temporary archive in the destination filesystem.
6. Start the dump process and stream stdout through Zstandard.
7. Capture bounded, sanitized stderr and the process exit code.
8. Flush and synchronize the archive and checksum.
9. Atomically publish the archive and metadata only after success.
10. Append a local execution-history record.
11. Apply retention only after a successful backup.

Each execution starts with a lock-scoped history reconciliation. A `running`
record is only changed to `interrupted` when the profile lock can be acquired,
proving that no live process owns it. Execution age is never used as evidence.

Schedule changes are coordinated with configuration updates:

1. Validate the generated calendar and unit files.
2. Snapshot the affected profile's unit files and enabled/active state.
3. Disable and verify the old timer.
4. Save the new configuration.
5. Enable or remove the desired timer and verify the resulting systemd state.
6. On failure, restore the prior configuration and timer snapshot.

This transaction is limited to the selected profile. Unrelated unit files are
not changed.

Systemd state is queried with `LoadState`, `ActiveState`, and
`UnitFileState`. A legitimate `not-found` unit is distinct from an unavailable
user manager, D-Bus failure, permission error, timeout, cancellation, malformed
output, or unexpected state. Unknown state fails the operation instead of being
treated as inactive.

Windows Task Scheduler state is queried through COM. A legitimate missing task
is distinct from RPC/service, permission, timeout, cancellation, or malformed
property failures. Snapshot/restore uses the registered task XML and enabled
state. Task names incorporate the canonical configuration path, Windows user SID
and profile. Ownership verification checks the principal, executable, working
directory, exact scheduler arguments, registration marker, and concurrency
settings. Task arguments contain no database password.

The scheduler supports interactive-token and native password logon. Password
mode passes the Windows credential directly to Task Scheduler registration and
does not persist it in FiveMesh files. S4U is excluded from the general backend
because Microsoft documents no network or encrypted-file access for that logon
type.

Rollback validates the saved snapshot and every live task-name occupant before
executing a mutation. A foreign task that appears after the snapshot makes
restoration fail closed rather than overwriting or deleting that task. Task
Scheduler lacks compare-and-swap registration, so a narrow check-to-COM race
remains documented rather than being represented as an atomic guarantee.

Rollback uses a separate 10-second recovery context. Cancelling the interactive
operation does not cancel cleanup, but recovery remains strictly bounded.

No SQL dump is buffered in memory and no uncompressed temporary SQL file is
created.

## Deliberate limits

- The MVP supports one database per profile. Multiple profiles provide multiple
  databases without introducing a multi-database restore format prematurely.
- Native client option coverage varies by client and server version. The core
  option set is conservative; unsupported version-specific options are not
  silently guessed. Invalid authentication and dump privileges surface as a
  failed backup with a sanitized client error.
- On Unix, native clients run in their own process group. Cancellation sends
  `SIGTERM`, escalates to `SIGKILL` after a short shutdown grace, waits with a
  bounded pipe delay, and reaps the direct child.
- On Windows, the direct client is assigned to a kill-on-close job object where
  the operating system permits.
- Verification checks the SHA-256 checksum and fully decodes the Zstandard
  stream. It is not a restore rehearsal.

## Consistency

`--single-transaction` is enabled by default and combined with
`--skip-lock-tables`. It provides a consistent snapshot for InnoDB tables when
the native client and server support it. It does not make MyISAM or other
nontransactional tables consistent, and concurrent DDL remains a limitation.
Dump clients include triggers by default; routines and events are configurable.

## Future boundaries

Optional cloud reporting can observe completed history records without becoming
part of the success condition for a local backup. Upload will operate on a
verified local archive and must not trigger another SQL dump. No cloud code is
part of this milestone.

## Linux scheduling decision

Per-user systemd units are the recommended production approach:

- no root-owned backup process;
- direct access to the installing user's credentials, state, and backup paths;
- no always-running agent;
- one timer/service pair per profile so one failure cannot disable another;
- `Persistent=true` so one missed run is caught up after boot;
- `loginctl enable-linger` ensures the user manager survives logout and reboot.

A system-wide service account remains an alternative for fleets that provision
accounts centrally, but it is not the default.

