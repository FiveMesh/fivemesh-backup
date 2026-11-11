# Security and reliability risks

## Current controls

- Dump commands use argument vectors, never a shell command string.
- Passwords are passed through short-lived client option files protected by
  Unix mode `0600` or a verified Windows DACL and are not placed in argv.
- Persistent credentials are stored separately from ordinary configuration and
  Unix-like systems reject group/world-readable credential files.
- Client stderr is bounded and sanitized before history/log persistence.
- Archive files are written to temporary siblings, compressed in a stream,
  synchronized, verified, and only then exposed through complete metadata.
- Each profile has its own advisory execution lock; independent profiles do not
  block one another.
- Abandoned `running` records are reconciled only after acquiring the matching
  profile lock; execution age is not used to declare a backup dead.
- Unix client cancellation terminates the process group and bounds inherited
  pipe waits. Windows uses a kill-on-close job object on a best-effort basis.
- Retention reads and checks complete metadata immediately before deletion,
  filters by profile, and always protects the newest successful archive.
- Restore verifies integrity, identifies source and target databases, warns
  about destructive effects, and requires an exact interactive target
  confirmation unless `--yes` is deliberately supplied.

- Discord webhook URLs are stored in the credentials file with the same
  protection as database passwords, are redacted from errors, and are shown
  masked. Outgoing messages disable all mention parsing except an explicitly
  configured failure mention, so error text cannot ping a server.

## Remaining risks

- The database password is stored in plaintext inside a permission-protected
  local file. Unix mode bits and Windows NTFS DACLs protect the file, but the
  same account, Administrator/SYSTEM, or malware running as that account can
  read it.
- A same-account attacker that can alter the archive, metadata, or checksum can
  replace all three consistently. SHA-256 detects accidental corruption, not a
  malicious rewrite.
- A process that deliberately escapes its process group/job cannot be reaped by
  the parent without platform sandboxing beyond this tool's scope.
- Schema DDL during a dump can still produce an inconsistent snapshot.
  `--single-transaction` does not cover MyISAM or other nontransactional tables.
- Verification proves archive checksum and Zstandard frame integrity. It does
  not prove SQL validity, object permissions, or application-level restore
  correctness.
- MariaDB/MySQL client/server option and privilege differences are broad. The
  MVP uses a conservative option set and surfaces native client errors rather
  than guessing unsupported options.
- Stored routines and events preserve definers. Restores may require `SET USER`
  or a reviewed definer strategy.
- Systemd user timers depend on lingering. The scheduler requests it and fails
  with an administrator command when policy denies the request.
- Schedule updates snapshot and verify systemd state. A failed activation,
  disable, reload, or removal triggers configuration/timer rollback or reports
  explicit manual recovery instructions.
- Manual-mode status inspection detects unit files or active timers that no
  longer match configuration.
- Unknown systemd state is never treated as confirmed inactive. D-Bus,
  permission, timeout, cancellation, malformed-output, and unexpected-state
  failures propagate to the caller.
- Windows background mode uses Task Scheduler S4U logon: the task runs as the
  configuring account without any stored Windows password, but has no Windows
  network credentials (no SMB shares or mapped drives; TCP connections to
  databases are unaffected). Network backup folders are rejected for this mode.
  Windows does not apply UAC token filtering to S4U tasks, so for an
  administrator account the scheduled backup runs with full administrator
  rights. Anything that can modify that account's FiveMesh configuration or
  executable could therefore run code at that level on the next schedule.
  Microsoft does not treat UAC as a security boundary; use a standard account
  where least privilege matters.
- Creating or updating a background task requires elevation. A non-elevated
  process asks for UAC consent and starts a hidden copy of the same executable
  with one validated request. The elevated helper only registers the exact task
  this installation would generate for the requested profile (name,
  description, principal, executable, arguments, and settings are checked) and
  refuses requests approved by a different Windows account. The task's security
  descriptor grants the owning account full control, so status, run, disable,
  and remove need no elevation afterwards.
- Windows Task Scheduler supports least-privilege interactive and password
  logon. Interactive mode requires an active logon session and stores no
  Windows password. Password mode prompts without echo and delegates storage to
  Task Scheduler as an LSA secret required for batch logon; FiveMesh never
  writes that password to TOML, XML, arguments, history, or logs.
- Password-mode execution requires `SeBatchLogonRight`. FiveMesh does not grant
  user rights automatically or fall back to Administrator/SYSTEM. Password
  changes require updating the stored task credential.
- During password-mode registration, the masked Windows password exists
  transiently in process memory and the COM call before Windows stores it as an
  LSA secret. Go strings are not guaranteed to be zeroed after use; the
  application clears its secret holder and never serializes or logs the value.
- Task Scheduler updates use the same bounded rollback transaction as systemd.
  Unknown COM/RPC state is never reported as successful deactivation.
- COM methods cannot be force-cancelled safely in-process after an RPC has
  started. Context cancellation is checked around each operation, but a wedged
  Task Scheduler service call may outlast the recovery deadline.
- Task Scheduler has no compare-and-swap update operation. Rollback performs
  identity checks immediately before mutation but cannot eliminate the narrow
  race where an unrelated task replaces the exact name between the final check
  and COM registration/removal.
- S4U is not offered: Microsoft documents that S4U provides no access to the
  network or encrypted files, so it is unsuitable as a general remote-database
  backup mode.
- Rollback runs with a detached, 10-second-bounded context after cancellation.
- Windows configuration, credential, rollback, and temporary client option
  files use protected DACLs limited to the current account, SYSTEM, and
  Administrators. Unrelated inherited ACEs are removed and the resulting ACL is
  verified before the operation continues.

Run as a dedicated non-root account, restrict backup-directory access, protect
the credentials file, and perform periodic restore rehearsals.

