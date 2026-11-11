# Debian and Ubuntu guide

FiveMesh Backup remains fully local. The Linux path uses a native CLI and
systemd user timers; it does not need Docker, a web server, or a FiveMesh account.

## Install

```sh
curl -fsSL https://github.com/FiveMesh/fivemesh-backup/releases/latest/download/install.sh | bash
```

The installer downloads the latest release for your CPU (amd64 or arm64),
verifies it against the release's `SHA256SUMS`, installs it to
`~/.local/bin/fivemesh-backup`, and starts setup. It refuses to run as root.
Re-run it to upgrade. Pin a version with `bash -s -- --version 1.0.0`.

To install a local build instead:

```sh
go build -trimpath -o bin/fivemesh-backup ./cmd/fivemesh-backup
bash install/install.sh --binary bin/fivemesh-backup --sha256 "$(sha256sum bin/fivemesh-backup | awk '{print $1}')"
```

## Configure

```sh
fivemesh-backup setup
```

The wizard asks for the engine, socket or TCP connection, credentials, database,
backup directory, compression, retention, minimum free space, and schedule.
Password entry is hidden. A successful setup offers a real backup followed by a
full archive integrity verification.

Configuration is stored under
`${XDG_CONFIG_HOME:-~/.config}/fivemesh-backup/`; credentials are mode `0600`.

## systemd user timers

The wizard offers manual, 6-hour, 12-hour, daily, or custom calendar scheduling.
Daily at 06:00 local time is the default. The timer expression includes the
selected IANA timezone, so daylight-saving changes are handled by systemd.
Generated intervals use component lists such as `*-*-* 00,06,12,18:00:00`.
`systemd-analyze calendar` validates every preset before a schedule can be saved.

Each profile receives independent files in `~/.config/systemd/user/`:

```text
fivemesh-backup-<profile>-<hash>.service
fivemesh-backup-<profile>-<hash>.timer
```

The service is a short-lived oneshot. It is not an agent. `Persistent=true`
causes at most one catch-up execution after downtime; it does not replay every
missed interval. The profile lock prevents overlap with manual or scheduled
executions.

Enabling a timer also checks `loginctl` lingering. If the current account may not
enable it, ask an administrator to run:

```sh
sudo loginctl enable-linger "$USER"
```

Commands:

```sh
fivemesh-backup schedule status
fivemesh-backup schedule enable --profile production
fivemesh-backup schedule next --profile production
fivemesh-backup schedule run --profile production
fivemesh-backup schedule logs --profile production --lines 100
fivemesh-backup schedule disable --profile production
fivemesh-backup schedule remove --profile production
```

`disable` keeps the unit definition so it can be enabled again. `remove` deletes
that profile's unit files and resets its configuration to manual mode.
Configuration writes a paused state only after the timer is confirmed inactive.
Reconfiguration pauses the previous timer, saves configuration, applies and
verifies the desired state, and rolls both sides back on failure.
If Ctrl-C occurs after a transaction starts, cleanup uses a separate bounded
recovery context so restoration is not rejected immediately by the cancelled
operation context.

## Reconfigure

```sh
fivemesh-backup configure
```

Existing profiles are loaded into the wizard. No configuration file is replaced
until the review is confirmed. Validation and connection failures leave the
prior configuration untouched.

## Logs and failures

```sh
fivemesh-backup status
fivemesh-backup logs --profile production
journalctl --user -u 'fivemesh-backup-*'
```

An execution left `running` by a crash is shown as `interrupted` after the
profile lock proves that no process still owns it. Recovery never reruns a
backup automatically and never deletes archives.

`schedule status` checks for stale timer units even when a profile is configured
for manual backups and returns a failure when configuration and systemd disagree.
If the systemd user manager cannot be queried, state is reported as unknown
rather than disabled.

## Uninstall

```sh
fivemesh-backup uninstall
```

This identifies schedules, disables and removes only configured profile units,
and optionally removes the executable with `--remove-binary`. It always
preserves configuration, credentials, execution history, and backup archives.

## Troubleshooting

- `systemctl --user` unavailable: log in through a systemd user session or use a
  disposable VM/service account with a user manager.
- Lingering denied: run the administrator command shown by the CLI.
- Database connection refused: verify socket/host, port, server status, and
  account grants.
- Scheduled backup still waiting after boot: the service remains in
  `waiting-for-database` and retries authenticated connectivity for up to two
  minutes. A readiness timeout is recorded as `database-readiness-timeout`.
- Routines cannot be dumped: grant the routine visibility privilege appropriate
  to the MariaDB/MySQL version, or disable routines in configuration.
- Systemd service fails: run `fivemesh-backup schedule logs`, then reproduce with
  `fivemesh-backup backup --profile <name>` from the same user.
- A MariaDB Unix socket placed in `/tmp` is hidden by the service's
  `PrivateTmp=true`. Use the normal `/run/mysqld/mysqld.sock` or customize the
  unit intentionally.

