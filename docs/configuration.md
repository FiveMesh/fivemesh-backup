# Configuration

By default, configuration is stored in the user configuration directory:

- Linux: `${XDG_CONFIG_HOME:-~/.config}/fivemesh-backup/config.toml`
- Windows: `%AppData%\fivemesh-backup\config.toml`

Credentials are stored beside it as `credentials.toml`. Set
`FIVEMESH_BACKUP_HOME` to override the directory for local testing.

On Windows, FiveMesh Backup applies and verifies a protected DACL granting full
access only to the current account, `SYSTEM`, and the built-in
`Administrators` group. Parent inheritance is disabled. Existing credentials
with unexpectedly broad ACLs are repaired before they are read; if the ACL
cannot be applied or verified, the operation fails closed.

## Example

```toml
version = 1
default_profile = "production"

[profiles.production]
description = "Production game database"

[profiles.production.database]
engine = "mysql"
flavor = "mariadb"
dump_tool = "auto"
client_tool = "auto"
host = "127.0.0.1"
port = 3306
username = "backup"
database = "fxserver"
single_transaction = true
include_routines = true
include_events = true

[profiles.production.backup]
directory = "/var/backups/fivemesh"
compression_level = 3
minimum_free_space_bytes = 536870912

[profiles.production.retention]
keep_last = 7
keep_days = 30
maximum_bytes = 214748364800

[profiles.production.schedule]
mode = "systemd"
on_calendar = "*-*-* 06:00:00"
timezone = "Europe/Brussels"
persistent = true
paused = false
```

The password file has the same profile key:

```toml
[profiles.production]
password = "replace-with-a-real-password"
```

Prefer `fivemesh-backup init` so the file is created with restrictive
permissions. Unix-like systems reject a credentials file readable by group or
other users.

## Timeouts

`backup.timeout` is optional and uses Go duration syntax, such as `"12h"`.
There is no default execution timeout because a legitimate large dump can run
unexpectedly long. Set a value only when the deployment has a known upper bound.

Before creating an archive, every execution waits for an authenticated
connection to the configured database. Availability errors are retried for up
to 120 seconds, or the overall backup timeout if shorter. Invalid credentials,
missing databases, and permission failures are not retried.

## Retention

- `keep_last` protects the newest N successful profile archives.
- `keep_days` protects successful archives created within that many days.
- `maximum_bytes` removes the oldest eligible archives until usage is below the
  cap, while always leaving the newest known successful archive.

All three values are optional. Setting all of them to zero disables automatic
retention. Cleanup only considers complete archives with matching metadata.
Unrelated files are never deleted.

## Scheduling

Existing configuration files without a schedule section remain valid and are
treated as manual mode. For Linux timers:

- `mode` is `manual` or `systemd`.
- `on_calendar` is a systemd calendar expression without the timezone suffix.
- `timezone` is an IANA timezone such as `Europe/Brussels`.
- `persistent = true` catches up at most once after missed execution windows.
- `paused = true` is written only after `schedule disable` confirms that the
  timer is both disabled and inactive.

Generated interval calendars use systemd component lists, for example
`*-*-* 00,06,12,18:00:00`, not comma-separated complete timestamps.

The `configure` wizard generates safe values for 6-hour, 12-hour, daily, and
custom schedules.

For Windows Task Scheduler:

```toml
[profiles.production.schedule]
mode = "windows"
persistent = true
paused = false

[profiles.production.schedule.windows]
frequency = "interval"
interval_minutes = 360
start_time = "06:30"
logon_mode = "background"
```

`frequency` is `daily` or `interval`. Interval values must be between 15 and
1440 minutes and divide evenly into 24 hours. Windows calendar triggers use the
machine's local timezone, so a Windows profile must leave `timezone` empty or
set it to `Local`. `on_calendar` is systemd-specific and must not be used in
Windows mode. See [windows-task-scheduler.md](windows-task-scheduler.md).

`logon_mode` accepts:

- `background`: runs whether or not you are signed in, with no stored
  password (Task Scheduler S4U). Created by setup by default. Needs one
  administrator approval when the task is created or changed, and a local
  backup folder.
- `interactive`: runs only while you are signed in. This is the value used
  when `logon_mode` is omitted, for compatibility with older files.
- `password`: runs unattended using your Windows password, which is prompted at
  task registration, stored by Task Scheduler as an LSA secret, and never
  serialized into configuration. Required for network-share backup folders.

## Notifications

`[notifications.discord]` controls Discord messages: `enabled`, `events`
(`success`, `failure`, `restore`, `config`; empty means all), `server_name`,
and `mention_on_failure`. The webhook URL is stored in `credentials.toml` under
`[notifications] discord_webhook_url`, never in `config.toml`. Use
`fivemesh-backup notify discord` rather than editing these by hand. See
[notifications.md](notifications.md).

## Database privilege notes

For a `mariadb-dump` backup that includes views, triggers, routines, and events,
the source account generally needs `SELECT`, `SHOW VIEW`, `TRIGGER`,
`LOCK TABLES`, `EVENT`, and routine visibility privileges on the relevant
objects. MariaDB versions with granular routine privileges require
`SHOW CREATE ROUTINE`; older MySQL/MariaDB versions may use the corresponding
global or `mysql.proc` access instead. The native client error is preserved in
the sanitized run log when a privilege is missing.

Restoring routines or events also requires creation privileges on the target.
MariaDB archives preserve routine/event definers, so a non-root restore account
may require `SET USER`. Review that grant carefully; alternatively arrange for
the definer accounts to exist with suitable privileges.

The integration fixture documents the narrow test grants used for MariaDB 11.8.
They are not a universal production configuration.

