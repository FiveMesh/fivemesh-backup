# Windows credential and temporary-file security

FiveMesh Backup applies Windows-native discretionary access controls with
`golang.org/x/sys/windows`. It does not invoke `icacls` or shell commands.

## Protected objects

The following objects receive an explicit, protected DACL:

- The selected FiveMesh Backup configuration directory.
- The separate credentials directory, if different.
- The state directory used for locks, history, and sanitized logs.
- `config.toml` and `credentials.toml`.
- Temporary configuration and credential replacement files.
- Snapshot files used during reconfiguration rollback.
- Temporary native-client option files containing username/password data.

Parent directories above the selected configuration, credentials, or state
directory are not modified.

## Allowed principals

The DACL grants full control only to:

- The current process account SID.
- `NT AUTHORITY\SYSTEM`.
- `BUILTIN\Administrators`.

The current process account is also set as the object owner. This matters on
Windows because an object's owner can change its DACL even when the DACL does
not grant that owner access. Inheritance from broad parent ACLs is disabled.
After applying the ACL, the code reads the security descriptor and verifies:

- The owner is the current account.
- The DACL is protected.
- No inherited or deny ACEs are present.
- No extra principals or duplicate ACEs are present.
- Sensitive files have full-control access for each required principal.
- Sensitive directories have both the effective and inherit-only ACE expected
  from Windows for each required principal.

## Failure behavior

ACL application or verification failures stop the configuration operation. If a
credentials replacement has already occurred, the prior credential and
configuration snapshots are restored through the same protected path. Temporary
files are removed on both success and failure.

Existing credentials with a broad DACL are repaired before they are read. If the
ACL cannot be repaired, reading or updating credentials fails closed.
Symbolic links and Windows reparse points or junctions are rejected before ACL
application so a path cannot redirect protection to another object.

## Threat model

These controls protect credentials from other standard local accounts and remove
overly broad inherited permissions. They do not protect against:

- Malware or another process already running as the same account.
- Administrators or SYSTEM.
- Offline disk access or an attacker who can take ownership/alter ACLs.
- Memory inspection of a running process.

Credentials remain plaintext in a restricted local file; DPAPI or an OS
credential store is not used.

If protection fails, confirm that the configured path is on a local NTFS volume,
that the backup account owns the sensitive files or can take ownership, and that
the account can write their security descriptors. FiveMesh Backup stops the
operation rather than retaining a readable credentials path.

## Test evidence

Windows-native tests create files with spaces and Unicode characters, apply and
read back ACLs, verify principal sets, repair a broad Everyone ACL, exercise
configuration replacement rollback, inspect temporary option files, and confirm
cleanup after client-start failure. ACL access-denied behavior is tested through
a deterministic Windows API failure injection, and reparse-point rejection is
tested with a disposable junction.

