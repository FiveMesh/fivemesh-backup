# Windows Task Scheduler

FiveMesh Backup uses the native Windows Task Scheduler COM service. It does not
use `schtasks.exe`, create a background agent, or require Docker.

## Execution models

FiveMesh supports three current-account modes: background (recommended),
interactive, and password. It never switches the task to another account or to
SYSTEM. Only background mode asks for elevation, once, when the task is
created or changed.

### Interactive session

`logon_mode = "interactive"` uses Task Scheduler's `InteractiveToken` with
`LeastPrivilege`. No Windows password is stored. The task runs only while the
current account has an active interactive logon session. This mode remains
available for workstations and attended servers, but is not adequate for a
headless 24/7 Windows Server.

### Unattended password mode

`logon_mode = "password"` is for backups to network shares, which background
mode cannot reach. It uses Task
Scheduler's native password logon. FiveMesh prompts for the current Windows
account password without echo and passes it directly to Task Scheduler COM at
registration. Task Scheduler stores it as an LSA secret for batch logon;
FiveMesh does not write it to TOML, task XML, arguments, history, or logs.

The account must hold `SeBatchLogonRight` (`Log on as a batch job`). Windows may
assign it automatically when an administrator creates a password task, but
FiveMesh does not elevate to change local security policy. The task runs in the
account's security context after logout and reboot.

Password logon preserves the normal Windows credential context, so native
MariaDB/MySQL clients can connect to local or remote TCP database endpoints,
subject to the host firewall, database grants, and the configured native client.
The database password remains separate and continues to use the restricted
option-file mechanism.

Run configuration under the account that will execute backups. The configuration
ACL owner and allow-list are bound to that account, so registering a task for a
different user without reapplying configuration ACLs will fail closed. A
dedicated standard local backup account is recommended.

### Background (recommended)

`logon_mode = "background"` is the default for new schedules. It uses Task
Scheduler's S4U ("Run whether user is logged on or not" with "Do not store
password") logon for the current account:

- runs after sign-out and reboot, like a service;
- stores no Windows password anywhere;
- needs no extra Windows user and no `secpol.msc` changes;
- loads the account's profile, so configuration under `%AppData%` works.

S4U tasks have no Windows network credentials. They cannot reach SMB shares or
mapped drives, so FiveMesh rejects network backup folders in this mode. Plain
TCP connections are unaffected: local and remote databases connect normally,
and future HTTPS uploads would too. This was verified on Windows 11 and Windows
Server 2022 with a task that connected to `127.0.0.1:3306` and an HTTPS
endpoint while signed out.

Creating or updating an S4U task needs administrator rights. When the command
is not already elevated, FiveMesh shows a single UAC prompt and runs a hidden,
elevated copy of the same executable that registers only the exact task this
installation generates for the profile. The task is registered with a security
descriptor that grants the owning account full control, so `schedule status`,
`run`, `disable`, and `remove` work from a normal console afterwards. On
Windows Server's built-in Administrator account, which runs elevated, no prompt
appears.

Windows does not filter the token of S4U tasks, so a task for an administrator
account runs with administrator rights. See [security](security.md).

Each profile creates one task named
`FiveMesh Backup <profile-slug> <hash>`. The hash incorporates the canonical
configuration path, Windows user SID, and profile name so independent FiveMesh
installations cannot alias the same task. Task XML contains the executable, the
profile/config/credential/state paths, and command arguments. It never contains
the database password.
Before updating, disabling, running, removing, or snapshotting a task,
FiveMesh verifies the registration marker, URI, Windows principal, executable,
working directory, exact scheduler arguments, and concurrency/time-limit
settings. Forged descriptions or URIs are insufficient to claim ownership. An
unrelated task that happens to use the deterministic name is never overwritten
or deleted.

Tasks created by the earlier profile-only naming scheme are migrated only when
their action, paths, and principal identify the current installation. Foreign
legacy tasks are left untouched. `schedule status` directs an operator to run
`schedule update` when a verified legacy task remains.

The task action invokes the same short-lived `backup` command used manually.
Task Scheduler settings include:

- `MultipleInstancesPolicy=IgnoreNew`
- `ExecutionTimeLimit=PT0S` so large backups do not hit an arbitrary scheduler
  execution limit
- `StartWhenAvailable` when persistent missed execution is configured
- battery/network restrictions disabled

The backup engine's per-profile lock remains authoritative for overlap
protection.

## Schedule configuration

Windows scheduling uses `mode = "windows"` and a separate nested schedule:

```toml
[profiles.production.schedule]
mode = "windows"
persistent = true

[profiles.production.schedule.windows]
frequency = "interval"
interval_minutes = 360
start_time = "06:30"
logon_mode = "background"
```

Supported values:

- `frequency = "daily"` with `interval_minutes` omitted.
- `frequency = "interval"` with `interval_minutes` between `15` and `1440`.
  The interval must divide 24 hours evenly.

The wizard offers six-hour, twelve-hour, daily, and custom whole-hour intervals.
Daily at 06:00 is the default.

`logon_mode` is `background`, `interactive`, or `password`. The wizard and
`schedule configure` default to `background`; an omitted value in an existing
file remains `interactive` for backward compatibility. The unattended password is requested after review
and is never stored in FiveMesh configuration.

Task Scheduler interprets calendar triggers in the Windows machine's local
timezone. Daylight-saving transitions are therefore handled by Windows. The
current release does not translate arbitrary IANA timezone names on Windows;
the machine timezone is the selected timezone.

## Commands

```powershell
fivemesh-backup schedule status
fivemesh-backup schedule configure --profile production --frequency 6h \
  --start-time 06:00
fivemesh-backup schedule enable --profile production
fivemesh-backup schedule update --profile production
fivemesh-backup schedule next --profile production
fivemesh-backup schedule run --profile production
fivemesh-backup schedule logs --profile production --lines 100
fivemesh-backup schedule disable --profile production
fivemesh-backup schedule remove --profile production
```

`disable` keeps the task definition and marks it disabled only after Task
Scheduler confirms the state. `remove` deletes only the deterministic task name
for that profile and resets the profile to manual mode. `logs` reads the
application's sanitized local history logs, not Task Scheduler events.

Commands that may recreate a password-mode task during rollback request the
Windows password again. This includes some disable, remove, and reconfigure
transitions. The password exists only for that command and is cleared before
return.

`schedule configure` updates an existing profile without opening the wizard.
Every option has a default, so `schedule configure --profile production` alone
creates a daily 06:00 background schedule:

- `--frequency daily|6h|12h|custom`
- `--start-time HH:MM`
- `--logon-mode background|interactive|password` (default `background`)
- `--persistent true|false`
- `--interval-minutes N` only for `--frequency custom`

Paused state is cleared after successful activation. Database, credentials,
backup, retention, and other profile settings are preserved. Validation and
rollback use the same scheduler snapshot and configuration transaction as the
wizard.

## Password mode without a console (PowerShell Direct, remoting)

Background and interactive modes need no Windows password, so they work in any
session. Password mode reads the Windows password from the console; PowerShell
Direct and some remoting sessions do not provide one, and FiveMesh fails
closed rather than reading the password as plain text.

For those sessions, use `scripts/ps-direct-secure-config.ps1` from the
repository:

```powershell
.\scripts\ps-direct-secure-config.ps1 `
  -Executable "$env:LOCALAPPDATA\FiveMesh\bin\fivemesh-backup.exe" `
  schedule configure `
    --profile production `
    --frequency 6h `
    --start-time 06:00 `
    --logon-mode password
```

The script uses PowerShell's native `Read-Host -AsSecureString`, starts the
CLI's one-shot `password-broker`, writes UTF-16LE password bytes to the broker
through redirected standard input, and then launches the requested FiveMesh
command with a random pipe name. The password is never an argv value,
environment variable, file, TOML value, history entry, or log entry.

The named pipe:

- has a protected DACL containing only the current Windows user;
- rejects remote clients and additional instances;
- carries a bounded length-prefixed secret;
- exits after one use or after a 30-minute timeout.

The same wrapper can run other scheduling transitions:

```powershell
.\scripts\ps-direct-secure-config.ps1 `
  -Executable "$env:LOCALAPPDATA\FiveMesh\bin\fivemesh-backup.exe" `
  schedule enable --profile production
```

## Transactions and recovery

Reconfiguration validates the generated task XML through the Task Scheduler COM
parser before changing configuration. It snapshots the previous task XML,
enabled state, and existence; pauses the old task; persists configuration;
registers/updates the desired task; and verifies that it exists, is enabled, and
has a next run time.

Failure or cancellation after a transition starts uses the existing detached,
10-second-bounded recovery context. The prior task and configuration are
restored when recovery is possible. If restoration cannot be verified, the CLI
reports an incomplete rollback and prints recovery guidance.

Rollback validates both the saved snapshot and the live occupant of every
affected task name before mutating anything. If a foreign task now occupies one
of those names, rollback fails closed and leaves it untouched. Task Scheduler
does not expose a compare-and-swap registration API, so a narrow time-of-check
to time-of-use race remains between the final identity check and the COM
mutation. FiveMesh rechecks immediately before mutation but cannot claim a
strict atomic guarantee against an attacker replacing the exact task name in
that interval.

Unknown Task Scheduler state is never reported as successful deactivation.
COM/RPC failures, timeouts, permission errors, and malformed task state fail
closed.

Cancellation is checked before each mutation and after every COM operation. The
Windows COM API does not expose a portable way to interrupt one already-running
Task Scheduler RPC in the same process; therefore the 10-second recovery
deadline bounds FiveMesh's recovery workflow but cannot forcibly abort a wedged
operating-system RPC. A query that cannot complete remains an unknown state and
must be investigated before retrying.

## Logon-mode limitations

- Interactive mode does not run while the user is signed out.
- Password mode depends on the account's stored LSA credential and
  `SeBatchLogonRight`; changing the Windows password requires updating the task.
- Background mode cannot write to network shares or mapped drives.
- SYSTEM execution, service-account logon, and managed service accounts are
  not implemented.
- FiveMesh never falls back from password mode to Administrator or SYSTEM.
- Missed executions are governed by Task Scheduler. `StartWhenAvailable` does
  not promise immediate catch-up; Windows may delay it by about ten minutes.
- If the executable is moved or replaced, run `configure` or
  `schedule update` to rewrite the task action.
- Uninstalling FiveMesh Backup removes only configured/residual FiveMesh tasks.
  Archives, credentials, configuration, and history are preserved.

## Granting batch logon on Server Core

The password-mode account must be allowed to log on as a batch job. On a
disposable server, an administrator can use Local Security Policy (`secpol.msc`)
where available. Server Core can use `secedit`:

```powershell
whoami /user
secedit /export /cfg "$env:TEMP\fivemesh-security.inf"
# Add the operator SID to SeBatchLogonRight in the exported file.
secedit /configure /db "$env:TEMP\fivemesh-security.sdb" `
  /cfg "$env:TEMP\fivemesh-security.inf" /areas USER_RIGHTS
```

Review the exported policy and retain any existing SIDs; replacing
`SeBatchLogonRight` instead of extending it can break unrelated scheduled jobs.
FiveMesh does not run this command or request elevation.

## Troubleshooting

```powershell
fivemesh-backup schedule status --profile production
Get-ScheduledTask -TaskName 'FiveMesh Backup*' |
  Select-Object TaskName, State, TaskPath
Get-ScheduledTaskInfo -TaskName 'FiveMesh Backup <profile> <hash>'
```

If Task Scheduler COM is unavailable, status is unknown and commands fail
instead of claiming that scheduling is disabled. Verify the Task Scheduler
service and task permissions. `schedule status` displays `last_run` and
`last_result`; Task Scheduler result `0x41303` means the task has not run yet.
`schedule run` waits briefly and reports an error when Task Scheduler accepts
the request but the task never starts. The Task Scheduler never-run sentinel
(historically displayed as `1999-11-30`) is omitted from `last_run`.

Task Scheduler calendar values are local wall-clock values without an offset.
FiveMesh reconstructs COM `NextRunTime` in the Windows local timezone before
formatting it, preventing the previous extra UTC-offset shift.

## Microsoft references

- [TASK_LOGON_TYPE](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_logon_type)
- [RegisterTaskDefinition](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/nf-taskschd-itaskfolder-registertaskdefinition)
- [Security contexts for tasks](https://learn.microsoft.com/en-us/windows/win32/taskschd/security-contexts-for-running-tasks)
- [Log on as a batch job](https://learn.microsoft.com/en-us/windows/security/threat-protection/security-policy-settings/log-on-as-a-batch-job)
- [Scheduled-task credentials and LSA](https://learn.microsoft.com/en-us/windows-server/identity/securing-privileged-access/reference-tools-logon-types)
