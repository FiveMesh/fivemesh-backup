# Testing

## Automated tests

```sh
gofmt -l .
go vet ./...
go test ./...
```

The unit suite covers strict configuration, atomic reconfiguration, credential
permissions, archive round trips, corruption detection, partial cleanup,
native-client argument construction, parent/descendant cancellation, reaping,
per-profile locks, abandoned-execution recovery, wizard cancellation, secret
redaction, systemd unit generation/lifecycle, installer integrity, backup
failure cleanup, restore verification, and retention safety.
Windows scheduler tests also cover generated task XML, preset/custom cadence,
daily/interval behavior, DST boundary construction, profile task naming,
interactive-token least-privilege principals, missing tasks, unavailable
service/query failures, snapshot validation, cancellation, and verified
disable/remove/restore transitions. Installation-isolation tests use two
canonical configuration homes with the same profile name and verify that task
names, task actions, disable, and removal cannot cross installation boundaries.
Rollback tests insert foreign tasks after both present-task and absent-task
snapshots and verify that restoration fails closed without mutation. Legacy
profile-only task names are migrated only after executable, argument, principal,
and configuration identity checks pass.
Additional tests cover password-mode registration, absence of Windows passwords
in TOML/XML/history, password requirements during rollback, Task Scheduler
local wall-clock conversion across winter/summer and DST boundaries, and
manual-run requests that Task Scheduler accepts but never starts.
Background (S4U) tests cover task XML, owner security descriptors, password-free
registration and snapshots, rejection of network backup folders, and the
profile identity used by the elevated registration helper. Wizard tests cover
the database picker, credential failures during listing, preset preselection,
and byte-order-mark handling in piped answers.
Windows tests also exercise the one-shot password pipe: protected current-user
DACL, round-trip delivery, timeout cancellation, invalid names, broker stderr
redaction, and non-terminal prompt failure. CI parses the PowerShell Direct
helper with the Windows PowerShell parser.

## Disposable MariaDB acceptance test

Docker is optional and used only by the development fixture:

```sh
bash test/integration/run.sh
```

See `test/integration/README.md` in the repository. The test must never target a
production database.

## Manual MariaDB acceptance evidence

The initial implementation was exercised against a disposable MariaDB 11.8.6
instance on loopback using `mariadb-dump` and `mariadb` clients. That run
covered:

- 50,000 generated rows and mixed InnoDB/MyISAM tables
- a view, procedure, function, trigger, and scheduled event
- successful compressed backup, checksum verification, and local history
- restore into a separate target database with matching 50,000 rows and object
  counts
- empty database backup
- corrupted archive rejection
- invalid credential rejection without publishing an archive
- insufficient routine privileges rejection without publishing an archive
- `keep_last = 2` retention after three real backups
- password absence from execution logs in the invalid-credential case

This evidence does not replace running the reproducible Docker script or a
Linux acceptance test.

## Linux checks without installing services

- `systemd-analyze` validates generated service/timer files in Linux tests.
- Every generated interval calendar is passed to `systemd-analyze calendar`,
  including arbitrary start times and daylight-saving transitions.
- systemctl/loginctl enable, disable, remove, logging, and next-time operations
  are tested with mocked command execution.
- Failure injection covers unit creation, reload, activation, disable, removal,
  unconfirmed activity, stale manual-mode timers, and configuration rollback.
- Readiness tests cover Unix socket and TCP recovery, permanent credential and
  database errors, bounded timeout, cancellation, and archive suppression.
- State-query tests distinguish enabled/active, disabled/inactive, legitimate
  not-found, D-Bus failure, permission denial, timeout, cancellation, malformed
  output, and unexpected states.
- Cancellation tests cover before the transaction, after timer pause, during
  persistence/activation, during removal, and failure of rollback itself.
- The local installer test builds a Linux binary, installs it into a temporary
  prefix, rejects a bad checksum, uninstalls it, and verifies that user data is
  preserved.
- The Windows installer test performs the equivalent per-user install, upgrade,
  checksum rejection, uninstall, and user-data preservation flow, without
  changing the user's PATH.
- Actual persistent timer registration is intentionally not performed on the
  development host. It requires a disposable Linux VM or explicit authorization.
- A transient user service with the production hardening directives successfully
  executed the backup binary, read configuration and credentials, wrote state and
  archive files, and connected through a Unix socket.
- Native Windows tests inspect actual DACLs for configuration directories,
  credentials, reconfiguration/rollback temporaries, and native-client option
  files, including Unicode/space paths and repair of broad ACLs.
  The socket probes are in `test/systemd/`.

## Windows Task Scheduler checks

Normal Windows CI validates generated XML with the native Task Scheduler COM
parser and queries a legitimately missing task without registering anything.
An explicitly guarded lifecycle test can register a disposable task, disable
it, snapshot/restore its state, and delete it:

```powershell
$env:FIVEMESH_TEST_TASK_SCHEDULER = "1"
go test -count=1 -run TestWindowsTaskSchedulerLifecycleIntegration ./internal/taskscheduler
```

Run that command only on a disposable Windows account.

Before a release, also check on a disposable Windows Server VM: setup with a
background schedule, a scheduled run while signed out, a reboot with a missed
run caught up, disable and re-enable, and a restore into a separate database.

