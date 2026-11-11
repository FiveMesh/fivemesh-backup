package systemd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

type commandCall struct {
	Name string
	Args []string
}

type fakeRunner struct {
	Calls          []commandCall
	Err            error
	Enabled        bool
	Active         bool
	Missing        bool
	IgnoreStart    bool
	StateErr       error
	StateOutput    string
	StateFailAfter int
	StateQueries   int
	Malformed      bool
	FailMatch      string
	Failed         bool
	StuckActive    bool
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.Calls = append(r.Calls, commandCall{Name: name, Args: append([]string(nil), args...)})
	if r.FailMatch != "" && !r.Failed && containsArg(args, r.FailMatch) {
		r.Failed = true
		return nil, errors.New("injected " + r.FailMatch + " failure")
	}
	if r.Err != nil {
		return nil, r.Err
	}
	if name == "systemctl" {
		switch {
		case containsArg(args, "show") && containsArg(args, "--property=LoadState"):
			r.StateQueries++
			if r.StateErr != nil && (r.StateFailAfter == 0 || r.StateQueries > r.StateFailAfter) {
				return nil, r.StateErr
			}
			if r.Malformed {
				return []byte("this is not property output\n"), nil
			}
			if r.StateOutput != "" {
				return []byte(r.StateOutput), nil
			}
			if r.Missing {
				return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=\n"), nil
			}
			unitFileState := "disabled"
			if r.Enabled {
				unitFileState = "enabled"
			}
			activeState := "inactive"
			if r.Active || r.StuckActive {
				activeState = "active"
			}
			return []byte("LoadState=loaded\nActiveState=" + activeState + "\nUnitFileState=" + unitFileState + "\n"), nil
		case containsArg(args, "disable"):
			r.Enabled = false
			if containsArg(args, "--now") {
				r.Active = false
			}
		case containsArg(args, "enable"):
			r.Enabled = true
		case containsArg(args, "restart"), containsArg(args, "start"):
			if !r.IgnoreStart {
				r.Active = true
			}
		case containsArg(args, "stop"):
			r.Active = false
		case containsArg(args, "show"):
			return []byte("1800000000000000\n"), nil
		}
	}
	return nil, nil
}

func TestUnitNamesAreStableAndProfileIsolated(t *testing.T) {
	serviceA, timerA, err := UnitNames("production")
	if err != nil {
		t.Fatal(err)
	}
	serviceB, timerB, err := UnitNames("staging")
	if err != nil {
		t.Fatal(err)
	}
	if serviceA == serviceB || timerA == timerB {
		t.Fatalf("profiles share units: %s %s", serviceA, serviceB)
	}
	if !strings.HasSuffix(serviceA, ".service") || !strings.HasSuffix(timerA, ".timer") {
		t.Fatalf("unexpected unit names: %s %s", serviceA, timerA)
	}
}

func TestGenerateUnitsIncludesIsolationTimezoneAndPersistence(t *testing.T) {
	root := t.TempDir()
	scheduler := &Scheduler{
		UnitDir:    filepath.Join(root, "units"),
		BinaryPath: filepath.Join(root, "bin", "fivemesh-backup"),
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Command: &fakeRunner{},
	}
	profile := config.Profile{
		Backup: config.Backup{Directory: filepath.Join(root, "backups")},
		Schedule: config.Schedule{
			Mode:       "systemd",
			OnCalendar: "*-*-* 06:00:00",
			Timezone:   "Europe/Brussels",
			Persistent: true,
		},
	}
	service, timer, err := scheduler.Generate("production", profile)
	if err != nil {
		t.Fatal(err)
	}
	serviceText := string(service)
	timerText := string(timer)
	if !strings.Contains(serviceText, `--profile=\"production\"`) &&
		!strings.Contains(serviceText, `"--profile=production"`) {
		t.Fatalf("profile is missing from service ExecStart: %s", serviceText)
	}
	if !strings.Contains(serviceText, "NoNewPrivileges=true") ||
		!strings.Contains(serviceText, "ProtectSystem=strict") {
		t.Fatalf("service hardening is missing: %s", serviceText)
	}
	if !strings.Contains(timerText, "OnCalendar=*-*-* 06:00:00 Europe/Brussels") {
		t.Fatalf("calendar timezone is missing: %s", timerText)
	}
	if !strings.Contains(timerText, "Persistent=true") {
		t.Fatalf("persistent timer is not enabled: %s", timerText)
	}
}

func TestEnableWritesUnitsAndReloadsUserManager(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	binary := filepath.Join(root, "fivemesh-backup")
	if err := os.WriteFile(binary, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	scheduler := &Scheduler{
		UnitDir:    filepath.Join(root, "units"),
		BinaryPath: binary,
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Command:     runner,
		CurrentUser: "tester",
	}
	profile := config.Profile{
		Backup: config.Backup{Directory: filepath.Join(root, "backups")},
		Schedule: config.Schedule{
			Mode:       "systemd",
			OnCalendar: "*-*-* 06:00:00",
			Persistent: true,
		},
	}
	next, err := scheduler.Enable(context.Background(), "production", profile)
	if err != nil {
		t.Fatal(err)
	}
	if next == "" {
		t.Fatal("next execution time is empty")
	}
	service, timer, err := UnitNames("production")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(scheduler.UnitDir, service),
		filepath.Join(scheduler.UnitDir, timer),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unit %s was not written: %v", path, err)
		}
	}
	if !callsContain(runner.Calls, "systemctl", "--user", "daemon-reload") {
		t.Fatal("daemon-reload was not called")
	}
	if !callsContain(runner.Calls, "systemctl", "--user", "enable", "--now", timer) {
		t.Fatal("timer was not enabled")
	}
	if !callsContain(runner.Calls, "loginctl", "enable-linger", "tester") {
		t.Fatal("user lingering was not enabled for reboot persistence")
	}
	profile.Schedule.OnCalendar = "*-*-* 07:00:00"
	if _, err := scheduler.Enable(context.Background(), "production", profile); err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	timerData, err := os.ReadFile(filepath.Join(scheduler.UnitDir, timer))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timerData), "OnCalendar=*-*-* 07:00:00") {
		t.Fatalf("updated calendar was not written: %s", timerData)
	}
}

func TestDisableKeepsUnitDefinitions(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	runner.Enabled = true
	runner.Active = true
	scheduler := &Scheduler{
		UnitDir: filepath.Join(root, "units"), BinaryPath: filepath.Join(root, "fivemesh-backup"),
		Paths:   config.Paths{Config: filepath.Join(root, "config.toml"), Credentials: filepath.Join(root, "credentials.toml"), StateDir: filepath.Join(root, "state")},
		Command: runner,
	}
	if err := os.MkdirAll(scheduler.UnitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service, timer, _ := UnitNames("production")
	for _, name := range []string{service, timer} {
		if err := os.WriteFile(filepath.Join(scheduler.UnitDir, name), []byte("unit"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := scheduler.Disable(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scheduler.UnitDir, timer)); err != nil {
		t.Fatalf("disabled timer unit was removed: %v", err)
	}
	if !callsContain(runner.Calls, "systemctl", "--user", "disable", "--now", timer) {
		t.Fatal("timer was not disabled")
	}
}

func TestRemoveDeletesOnlyProfileUnits(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	scheduler := &Scheduler{
		UnitDir:    filepath.Join(root, "units"),
		BinaryPath: filepath.Join(root, "fivemesh-backup"),
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Command: runner,
	}
	if err := os.MkdirAll(scheduler.UnitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service, timer, _ := UnitNames("production")
	otherService, otherTimer, _ := UnitNames("staging")
	for _, name := range []string{service, timer, otherService, otherTimer} {
		if err := os.WriteFile(filepath.Join(scheduler.UnitDir, name), []byte("unit"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := scheduler.Remove(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scheduler.UnitDir, service)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("profile service was not removed")
	}
	if _, err := os.Stat(filepath.Join(scheduler.UnitDir, timer)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("profile timer was not removed")
	}
	if _, err := os.Stat(filepath.Join(scheduler.UnitDir, otherTimer)); err != nil {
		t.Fatalf("other profile timer was removed: %v", err)
	}
}

func TestEnableFailuresRestorePreviousState(t *testing.T) {
	for _, failMatch := range []string{"daemon-reload", "enable", "restart"} {
		t.Run(failMatch, func(t *testing.T) {
			scheduler, profile, runner := newFailureScheduler(t, failMatch)
			snapshot, err := scheduler.Snapshot(context.Background(), "production")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil {
				t.Fatalf("Enable succeeded despite injected %s failure", failMatch)
			}
			if err := scheduler.Restore(context.Background(), "production", snapshot); err != nil {
				t.Fatalf("restore after %s failure: %v", failMatch, err)
			}
			service, timer, _ := UnitNames("production")
			for _, path := range []string{
				filepath.Join(scheduler.UnitDir, service),
				filepath.Join(scheduler.UnitDir, timer),
			} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial unit survived restore: %s", path)
				}
			}
			if runner.Enabled || runner.Active {
				t.Fatal("timer remained enabled or active after rollback")
			}
		})
	}
}

func TestDisableFailureCanRestoreActiveTimer(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "disable")
	if err := writeExistingUnits(scheduler, profile); err != nil {
		t.Fatal(err)
	}
	runner.Enabled = true
	runner.Active = true
	snapshot, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Disable(context.Background(), "production"); err == nil {
		t.Fatal("Disable succeeded despite injected failure")
	}
	if err := scheduler.Restore(context.Background(), "production", snapshot); err != nil {
		t.Fatalf("restore active timer: %v", err)
	}
	if !runner.Enabled || !runner.Active {
		t.Fatal("active timer state was not restored")
	}
}

func TestRemoveReloadFailureCanRestoreUnits(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "daemon-reload")
	if err := writeExistingUnits(scheduler, profile); err != nil {
		t.Fatal(err)
	}
	runner.Enabled = true
	runner.Active = true
	snapshot, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Remove(context.Background(), "production"); err == nil {
		t.Fatal("Remove succeeded despite injected reload failure")
	}
	if err := scheduler.Restore(context.Background(), "production", snapshot); err != nil {
		t.Fatalf("restore removed units: %v", err)
	}
	service, timer, _ := UnitNames("production")
	for _, path := range []string{
		filepath.Join(scheduler.UnitDir, service),
		filepath.Join(scheduler.UnitDir, timer),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unit was not restored: %s", path)
		}
	}
	if !runner.Enabled || !runner.Active {
		t.Fatal("timer state was not restored after removal failure")
	}
}

func TestUnitCreationFailureIsReported(t *testing.T) {
	scheduler, profile, _ := newFailureScheduler(t, "")
	if err := os.WriteFile(scheduler.UnitDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil {
		t.Fatal("Enable succeeded when the unit directory was not writable")
	}
}

func TestEnableRejectsUnconfirmedTimerActivation(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "")
	runner.IgnoreStart = true
	if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil {
		t.Fatal("Enable reported success without systemd confirming timer activity")
	}
	if !runner.Enabled {
		t.Fatal("test expected the timer to be enabled before activity verification")
	}
}

func TestDisableRejectsTimerThatRemainsActive(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "")
	if err := writeExistingUnits(scheduler, profile); err != nil {
		t.Fatal(err)
	}
	runner.Enabled = true
	runner.Active = true
	runner.StuckActive = true
	if err := scheduler.Disable(context.Background(), "production"); err == nil {
		t.Fatal("Disable reported success while systemd still reported the timer active")
	}
}

func TestStatusDetectsManualStaleAndPausedStates(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "")
	service, timer, _ := UnitNames("production")
	if err := writeExistingUnits(scheduler, profile); err != nil {
		t.Fatal(err)
	}
	runner.Enabled = true
	runner.Active = true
	manual := profile
	manual.Schedule = config.Schedule{Mode: "manual"}
	status := scheduler.Status(context.Background(), "production", manual)
	if !status.Stale || status.UnitError == "" {
		t.Fatal("manual profile with active timer was not marked stale")
	}

	paused := profile
	paused.Schedule.Paused = true
	status = scheduler.Status(context.Background(), "production", paused)
	if !status.Stale {
		t.Fatal("paused profile with active timer was not marked stale")
	}
	runner.Enabled = false
	runner.Active = false
	status = scheduler.Status(context.Background(), "production", paused)
	if status.Stale {
		t.Fatalf("correctly paused timer was marked stale: %#v", status)
	}
	runner.Enabled = true
	runner.Active = true
	status = scheduler.Status(context.Background(), "production", profile)
	if status.Stale || status.Next == "" {
		t.Fatalf("active automatic timer was marked stale: %#v", status)
	}
	for _, path := range []string{
		filepath.Join(scheduler.UnitDir, service),
		filepath.Join(scheduler.UnitDir, timer),
	} {
		_ = os.Remove(path)
	}
	status = scheduler.Status(context.Background(), "production", profile)
	if !status.Stale {
		t.Fatal("missing units for an automatic profile were not marked stale")
	}
}

func TestQueryTimerStateDistinguishesKnownAndUnknownStates(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		err        error
		wantExists bool
		wantEnable bool
		wantActive bool
		wantErr    bool
	}{
		{
			name:       "enabled and active",
			output:     "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\n",
			wantExists: true,
			wantEnable: true,
			wantActive: true,
		},
		{
			name:       "disabled and inactive",
			output:     "LoadState=loaded\nActiveState=inactive\nUnitFileState=disabled\n",
			wantExists: true,
		},
		{
			name:   "unit legitimately missing",
			output: "LoadState=not-found\nActiveState=inactive\nUnitFileState=\n",
		},
		{
			name:    "dbus unavailable",
			err:     &CommandFailure{Command: "systemctl", Output: "Failed to connect to bus: unavailable", Err: errors.New("exit status 1")},
			wantErr: true,
		},
		{
			name:    "permission denied",
			err:     &CommandFailure{Command: "systemctl", Output: "Access denied", Err: errors.New("exit status 1")},
			wantErr: true,
		},
		{
			name:    "timeout",
			err:     context.DeadlineExceeded,
			wantErr: true,
		},
		{
			name:    "context cancellation",
			err:     context.Canceled,
			wantErr: true,
		},
		{
			name:    "malformed output",
			output:  "unexpected output\n",
			wantErr: true,
		},
		{
			name:    "unexpected state",
			output:  "LoadState=loaded\nActiveState=mystery\nUnitFileState=enabled\n",
			wantErr: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{StateOutput: testCase.output, StateErr: testCase.err}
			scheduler := &Scheduler{Command: runner}
			state, err := scheduler.queryTimerState(context.Background(), "probe.timer")
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("unknown state was accepted: %#v", state)
				}
				if testCase.err != nil && !errors.Is(err, testCase.err) {
					t.Fatalf("error does not preserve cause: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.Exists() != testCase.wantExists ||
				state.Enabled() != testCase.wantEnable ||
				state.Active() != testCase.wantActive {
				t.Fatalf("unexpected state: %#v", state)
			}
		})
	}
}

func TestSnapshotFailsWhenSystemdStateIsUnknown(t *testing.T) {
	scheduler, _, runner := newFailureScheduler(t, "")
	runner.StateErr = &CommandFailure{
		Command: "systemctl",
		Output:  "Failed to connect to bus: unavailable",
		Err:     errors.New("exit status 1"),
	}
	if _, err := scheduler.Snapshot(context.Background(), "production"); err == nil {
		t.Fatal("snapshot succeeded without a confirmed systemd state")
	}
}

func TestDisableFailsWhenInactivityCannotBeVerified(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "")
	if err := writeExistingUnits(scheduler, profile); err != nil {
		t.Fatal(err)
	}
	runner.Enabled = true
	runner.Active = true
	runner.StateErr = context.DeadlineExceeded
	runner.StateFailAfter = 1
	if err := scheduler.Disable(context.Background(), "production"); err == nil {
		t.Fatal("disable succeeded when post-command state was unknown")
	}
}

func TestStatusReportsUnavailableSystemdManager(t *testing.T) {
	scheduler, profile, runner := newFailureScheduler(t, "")
	runner.StateErr = &CommandFailure{
		Command: "systemctl",
		Output:  "Failed to connect to bus: unavailable",
		Err:     errors.New("exit status 1"),
	}
	status := scheduler.Status(context.Background(), "production", profile)
	if !status.Stale || status.QueryError == nil {
		t.Fatalf("unavailable manager was not reported: %#v", status)
	}
	if status.Mode != "systemd" {
		t.Fatalf("mode = %q, want systemd", status.Mode)
	}
	if !strings.Contains(status.UnitError, "cannot determine systemd timer state") {
		t.Fatalf("status error is not explicit: %s", status.UnitError)
	}
	legacyManual := profile
	legacyManual.Schedule.Mode = ""
	status = scheduler.Status(context.Background(), "production", legacyManual)
	if status.Mode != "manual" {
		t.Fatalf("empty schedule mode = %q, want manual", status.Mode)
	}
}

func newFailureScheduler(t *testing.T, failMatch string) (*Scheduler, config.Profile, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "fivemesh-backup")
	if err := os.WriteFile(binary, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{FailMatch: failMatch}
	scheduler := &Scheduler{
		UnitDir:    filepath.Join(root, "units"),
		BinaryPath: binary,
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Command:     runner,
		CurrentUser: "tester",
	}
	profile := config.Profile{
		Backup: config.Backup{Directory: filepath.Join(root, "backups")},
		Schedule: config.Schedule{
			Mode:       "systemd",
			OnCalendar: "*-*-* 06:00:00",
			Timezone:   "UTC",
			Persistent: true,
		},
	}
	return scheduler, profile, runner
}

func writeExistingUnits(scheduler *Scheduler, profile config.Profile) error {
	serviceData, timerData, err := scheduler.Generate("production", profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(scheduler.UnitDir, 0o755); err != nil {
		return err
	}
	service, timer, _ := UnitNames("production")
	if err := os.WriteFile(filepath.Join(scheduler.UnitDir, service), serviceData, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(scheduler.UnitDir, timer), timerData, 0o644)
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func callsContain(calls []commandCall, name string, args ...string) bool {
	for _, call := range calls {
		if call.Name != name || len(call.Args) != len(args) {
			continue
		}
		matches := true
		for index := range args {
			if call.Args[index] != args[index] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
