package wizard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/backup"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

type fakeScheduler struct {
	BackendMode       string
	Enabled           bool
	Disabled          bool
	Removed           bool
	Restored          bool
	SnapshotData      []byte
	Stale             bool
	Snapshotted       bool
	RestoreContextErr error
	LogonSecret       schedule.LogonSecret

	EnableErr         error
	DisableErr        error
	RemoveErr         error
	ValidateErr       error
	SnapshotErr       error
	RestoreErr        error
	ValidatedCalendar string
	ValidatedProfile  *config.Profile
	DisableHook       func()
	EnableHook        func()
	RemoveHook        func()
}

func (s *fakeScheduler) Mode() string {
	if s.BackendMode != "" {
		return s.BackendMode
	}
	return config.ScheduleModeSystemd
}

func (s *fakeScheduler) Manages(mode string) bool {
	return mode == s.Mode()
}

func (s *fakeScheduler) Enable(
	_ context.Context,
	_ string,
	_ config.Profile,
	secrets ...schedule.LogonSecret,
) (string, error) {
	if len(secrets) > 0 {
		s.LogonSecret = secrets[0]
	}
	if s.EnableHook != nil {
		s.EnableHook()
	}
	if s.EnableErr != nil {
		return "", s.EnableErr
	}
	s.Enabled = true
	return "2026-10-21T06:00:00+02:00", nil
}

func (s *fakeScheduler) Disable(context.Context, string) error {
	if s.DisableHook != nil {
		s.DisableHook()
	}
	if s.DisableErr != nil {
		return s.DisableErr
	}
	s.Disabled = true
	return nil
}

func (s *fakeScheduler) Remove(context.Context, string) error {
	if s.RemoveHook != nil {
		s.RemoveHook()
	}
	if s.RemoveErr != nil {
		return s.RemoveErr
	}
	s.Disabled = true
	s.Removed = true
	return nil
}

func (s *fakeScheduler) ValidateCalendar(_ context.Context, calendar string) error {
	s.ValidatedCalendar = calendar
	return s.ValidateErr
}

func (s *fakeScheduler) Validate(_ context.Context, _ string, profile config.Profile) error {
	copy := profile
	s.ValidatedProfile = &copy
	if profile.Schedule.Mode == config.ScheduleModeSystemd {
		s.ValidatedCalendar = profile.Schedule.OnCalendar
		if profile.Schedule.Timezone != "" {
			s.ValidatedCalendar += " " + profile.Schedule.Timezone
		}
	}
	return s.ValidateErr
}

func TestWindowsScheduleWizardPresetsAndLogonWarning(t *testing.T) {
	cases := []struct {
		name          string
		script        string
		wantFrequency string
		wantInterval  int
		wantStartTime string
		wantLogon     string
		wantOutput    string
	}{
		{
			name:          "six hours",
			script:        "2\n06:30\n2\n\n",
			wantFrequency: config.WindowsFrequencyInterval,
			wantInterval:  360,
			wantStartTime: "06:30",
			wantLogon:     config.WindowsLogonInteractive,
			wantOutput:    "signed in",
		},
		{
			name:          "daily defaults to background",
			script:        "4\n06:00\n\n\n",
			wantFrequency: config.WindowsFrequencyDaily,
			wantStartTime: "06:00",
			wantLogon:     config.WindowsLogonBackground,
			wantOutput:    "administrator approval once",
		},
		{
			name:          "custom divisor validation",
			script:        "5\n09:15\n5\n3\n2\n\n",
			wantFrequency: config.WindowsFrequencyInterval,
			wantInterval:  180,
			wantStartTime: "09:15",
			wantLogon:     config.WindowsLogonInteractive,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scheduler := &fakeScheduler{BackendMode: config.ScheduleModeWindows}
			var output bytes.Buffer
			profile := config.Profile{}
			err := configureSchedule(
				context.Background(),
				NewPrompter(strings.NewReader(testCase.script), &output),
				"production",
				&profile,
				scheduler,
			)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Schedule.Mode != config.ScheduleModeWindows {
				t.Fatalf("mode = %q", profile.Schedule.Mode)
			}
			if profile.Schedule.OnCalendar != "" || profile.Schedule.Timezone != "" {
				t.Fatalf("Windows profile retained systemd schedule fields: %#v", profile.Schedule)
			}
			if profile.Schedule.Windows.EffectiveLogonMode() != testCase.wantLogon {
				t.Fatalf("execution mode = %q", profile.Schedule.Windows.EffectiveLogonMode())
			}
			if profile.Schedule.Windows.Frequency != testCase.wantFrequency ||
				profile.Schedule.Windows.IntervalMinutes != testCase.wantInterval ||
				profile.Schedule.Windows.StartTime != testCase.wantStartTime {
				t.Fatalf("unexpected Windows schedule: %#v", profile.Schedule.Windows)
			}
			if scheduler.ValidatedProfile == nil {
				t.Fatal("Windows task definition was not validated")
			}
			if testCase.wantOutput != "" && !strings.Contains(output.String(), testCase.wantOutput) {
				t.Fatalf("output is missing %q:\n%s", testCase.wantOutput, output.String())
			}
			if strings.Contains(strings.ToLower(output.String()), "password=") {
				t.Fatalf("wizard exposed password material:\n%s", output.String())
			}
		})
	}
}

func TestWindowsScheduleWizardSelectsUnattendedPasswordMode(t *testing.T) {
	scheduler := &fakeScheduler{BackendMode: config.ScheduleModeWindows}
	var output bytes.Buffer
	profile := config.Profile{}
	err := configureSchedule(
		context.Background(),
		NewPrompter(strings.NewReader("2\n06:00\n3\n\n"), &output),
		"production",
		&profile,
		scheduler,
	)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Schedule.Windows.EffectiveLogonMode() != config.WindowsLogonPassword {
		t.Fatalf("execution mode = %q", profile.Schedule.Windows.EffectiveLogonMode())
	}
	if !strings.Contains(output.String(), "Log on as a batch job") ||
		!strings.Contains(output.String(), "No password is placed") {
		t.Fatalf("unattended security guidance is missing:\n%s", output.String())
	}
}

func TestWindowsSecurePromptFailsClosedWithoutTerminal(t *testing.T) {
	var output bytes.Buffer
	_, err := NewPrompter(strings.NewReader("must-not-be-read\n"), &output).SecureSecret("Windows password")
	if err == nil {
		t.Fatal("non-terminal Windows password input was accepted")
	}
	if strings.Contains(output.String(), "must-not-be-read") {
		t.Fatal("insecure fallback echoed password material")
	}
}

func (s *fakeScheduler) Snapshot(context.Context, string) ([]byte, error) {
	if s.SnapshotErr != nil {
		return nil, s.SnapshotErr
	}
	s.SnapshotData = []byte("prior-state")
	s.Snapshotted = true
	return s.SnapshotData, nil
}

func (s *fakeScheduler) SnapshotRequiresPassword([]byte) bool {
	return false
}

func (s *fakeScheduler) Restore(ctx context.Context, _ string, snapshot []byte, _ ...schedule.LogonSecret) error {
	s.RestoreContextErr = ctx.Err()
	if s.RestoreContextErr != nil {
		return s.RestoreContextErr
	}
	if s.RestoreErr != nil {
		return s.RestoreErr
	}
	if string(snapshot) != "prior-state" {
		return errors.New("unexpected scheduler snapshot")
	}
	s.Restored = true
	return nil
}

func (s *fakeScheduler) Inspect(context.Context, string, config.Profile) (bool, string, error) {
	return s.Stale, "stale timer detected", nil
}

func TestWizardCreatesProfileAndRunsTestBackup(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backups")
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	script := strings.Join([]string{
		"",  // profile name: production
		"",  // MariaDB
		"2", // TCP
		"",  // host
		"",  // port
		"",  // username
		"very-secret",
		"fxserver", // database (listing unavailable, typed)
		backupDir,
		"",  // balanced compression
		"",  // recommended retention
		"",  // minimum free space
		"4", // daily schedule
		"",  // timezone default
		"",  // 06:00
		"",  // persistent
		"",  // save
		"y", // run backup
	}, "\n") + "\n"
	var output bytes.Buffer
	scheduler := &fakeScheduler{}
	result, err := Run(context.Background(), Options{
		Paths:         paths,
		StateDir:      paths.StateDir,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: false,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		RunBackup: func(_ context.Context, _ string, profile config.Profile, _ string, _ func(string)) (backup.Outcome, error) {
			return createFakeBackup(t, profile.Backup.Directory)
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, output.String())
	}
	if !result.ConfigCreated || !result.BackupExecuted || !result.Verified || result.NextScheduled == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !scheduler.Enabled {
		t.Fatal("systemd schedule was not enabled")
	}
	if strings.Contains(output.String(), "very-secret") {
		t.Fatal("password was echoed")
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles["production"].Schedule.Mode != "systemd" ||
		cfg.Profiles["production"].Database.Database != "fxserver" ||
		cfg.Profiles["production"].Schedule.OnCalendar != "*-*-* 06:00:00" ||
		!cfg.Profiles["production"].Schedule.Persistent {
		t.Fatalf("unexpected saved profile: %#v", cfg.Profiles["production"])
	}
	password, err := config.LoadCredential(paths.Credentials, "production")
	if err != nil || password != "very-secret" {
		t.Fatalf("credential was not saved: %q %v", password, err)
	}
}

func TestWizardPromptsForWindowsLogonPasswordWithoutPersistingIt(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backups")
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	script := strings.Join([]string{
		"", "", "", "", "", "",
		"database-secret", "fxserver",
		backupDir, "", "", "",
		"4", // daily
		"",  // 06:00
		"3", // unattended password mode
		"",  // persistent
		"",  // save
		"n", // do not run test backup
	}, "\n") + "\n"
	var output bytes.Buffer
	scheduler := &fakeScheduler{BackendMode: config.ScheduleModeWindows}
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		StateDir:      paths.StateDir,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: false,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
		WindowsPasswordInput: func(context.Context) (string, error) {
			return "windows-account-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.LogonSecret.WindowsPassword != "windows-account-secret" {
		t.Fatal("Windows password was not passed to the scheduler")
	}
	if strings.Contains(output.String(), "windows-account-secret") {
		t.Fatal("Windows password was echoed by the wizard")
	}
	configData, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	credentialData, err := os.ReadFile(paths.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "windows-account-secret") ||
		strings.Contains(string(credentialData), "windows-account-secret") {
		t.Fatal("Windows password was persisted in TOML files")
	}
}

func TestCalendarForInterval(t *testing.T) {
	if got := calendarForInterval(6, "06:30"); got != "*-*-* 00,06,12,18:30:00" {
		t.Fatalf("six-hour calendar = %q", got)
	}
	if got := calendarForInterval(12, "06:00"); got != "*-*-* 06,18:00:00" {
		t.Fatalf("twelve-hour calendar = %q", got)
	}
	if got := calendarForInterval(24, "23:45"); got != "*-*-* 23:45:00" {
		t.Fatalf("daily calendar = %q", got)
	}
}

func TestWizardValidatesEverySchedulePreset(t *testing.T) {
	cases := []struct {
		selection string
		clock     string
		expected  string
	}{
		{selection: "2", clock: "06:30", expected: "*-*-* 00,06,12,18:30:00 UTC"},
		{selection: "3", clock: "23:45", expected: "*-*-* 11,23:45:00 UTC"},
		{selection: "4", clock: "22:15", expected: "*-*-* 22:15:00 UTC"},
	}
	for _, testCase := range cases {
		t.Run(testCase.selection, func(t *testing.T) {
			scheduler := &fakeScheduler{}
			script := strings.Join([]string{
				testCase.selection,
				"UTC",
				testCase.clock,
				"",
			}, "\n") + "\n"
			var output bytes.Buffer
			profile := config.Profile{}
			if err := configureSchedule(
				context.Background(),
				NewPrompter(strings.NewReader(script), &output),
				"production",
				&profile,
				scheduler,
			); err != nil {
				t.Fatal(err)
			}
			if scheduler.ValidatedCalendar != testCase.expected {
				t.Fatalf("validated calendar = %q, want %q", scheduler.ValidatedCalendar, testCase.expected)
			}
			if profile.Schedule.OnCalendar != strings.TrimSuffix(testCase.expected, " UTC") {
				t.Fatalf("stored calendar = %q", profile.Schedule.OnCalendar)
			}
		})
	}
}

func TestWizardManualSwitchRollsBackWhenTimerRemovalFails(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{
		Mode:       "systemd",
		OnCalendar: "*-*-* 06:00:00",
		Timezone:   "Europe/Brussels",
		Persistent: true,
	}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{RemoveErr: errors.New("remove failed")}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "", // profile and database, keep password
		"", "", "", "", // storage
		"1", // manual
		"",  // save
		"n", // no test backup
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("timer removal failure was reported as success")
	}
	saved, loadErr := config.Load(paths.Config)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.Profiles["production"].Schedule.Mode != "systemd" {
		t.Fatalf("configuration was not rolled back: %#v", saved.Profiles["production"].Schedule)
	}
	if !scheduler.Restored {
		t.Fatal("previous timer state was not restored")
	}
}

func TestWizardScheduleActivationFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{EnableErr: errors.New("activation failed")}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "", // profile and database, keep password
		"", "", "", "", // storage
		"4", // daily systemd schedule
		"",  // timezone
		"",  // 06:00
		"",  // persistent
		"",  // save
		"n", // no test backup
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("activation failure was reported as success")
	}
	saved, loadErr := config.Load(paths.Config)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.Profiles["production"].Schedule.Mode == "systemd" {
		t.Fatalf("automatic schedule remained after rollback: %#v", saved.Profiles["production"].Schedule)
	}
	if !scheduler.Restored {
		t.Fatal("previous timer state was not restored")
	}
}

func TestNewProfileActivationFailureRemovesSavedConfiguration(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	backupDir := filepath.Join(root, "backups")
	script := strings.Join([]string{
		"production", // profile
		"",           // MariaDB
		"2",          // TCP
		"",           // host
		"",           // port
		"",           // username
		"secret",     // password
		"fxserver",   // database
		backupDir,    // storage
		"",           // compression
		"",           // retention
		"",           // minimum free
		"4",          // daily
		"",           // timezone
		"",           // time
		"",           // persistent
		"",           // save
		"n",          // no test backup
	}, "\n") + "\n"
	scheduler := &fakeScheduler{EnableErr: errors.New("activation failed")}
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: false,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("activation failure was reported as success")
	}
	for _, path := range []string{paths.Config, paths.Credentials} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("new configuration survived rollback: %s", path)
		}
	}
	if !scheduler.Restored {
		t.Fatal("empty prior scheduler state was not restored")
	}
}

func TestWizardDisableFailureLeavesConfigurationUntouched(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{Mode: "systemd", OnCalendar: "*-*-* 06:00:00", Timezone: "UTC"}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{DisableErr: errors.New("disable failed")}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("disable failure was reported as success")
	}
	saved, _ := config.Load(paths.Config)
	if saved.Profiles["production"].Schedule.Mode != "systemd" {
		t.Fatal("configuration changed before timer was paused")
	}
	if !scheduler.Restored {
		t.Fatal("timer state was not restored after disable failure")
	}
}

func TestWizardCancellationBeforeTransactionDoesNothing(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	backupDir := filepath.Join(root, "backups")
	script := strings.Join([]string{
		"production", "", "2", "", "", "", "secret", "fxserver", backupDir,
		"", "", "", "4", "", "", "", "", "n",
	}, "\n") + "\n"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := &fakeScheduler{}
	var output bytes.Buffer
	_, err := Run(ctx, Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: false,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if scheduler.Snapshotted || scheduler.Disabled || scheduler.Enabled || scheduler.Restored {
		t.Fatal("scheduler transaction started after cancellation")
	}
}

func TestWizardCancellationAfterPauseRestoresWithRecoveryContext(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{Mode: "systemd", OnCalendar: "*-*-* 06:00:00", Timezone: "UTC"}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := &fakeScheduler{DisableHook: cancel}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(ctx, Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !scheduler.Restored {
		t.Fatal("previous timer state was not restored")
	}
	if scheduler.RestoreContextErr != nil {
		t.Fatalf("rollback inherited cancelled context: %v", scheduler.RestoreContextErr)
	}
}

func TestWizardCancellationDuringActivationRollsBack(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{EnableErr: context.Canceled}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"4", "", "", "", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !scheduler.Restored || scheduler.RestoreContextErr != nil {
		t.Fatalf("scheduler rollback did not use clean recovery context: restored=%v err=%v", scheduler.Restored, scheduler.RestoreContextErr)
	}
	saved, _ := config.Load(paths.Config)
	if saved.Profiles["production"].Schedule.Mode == "systemd" {
		t.Fatal("configuration was not rolled back after activation cancellation")
	}
}

func TestWizardCancellationDuringRemovalRollsBack(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{Mode: "systemd", OnCalendar: "*-*-* 06:00:00", Timezone: "UTC"}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{RemoveErr: context.Canceled}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !scheduler.Restored || scheduler.RestoreContextErr != nil {
		t.Fatal("timer was not restored with a clean recovery context")
	}
}

func TestWizardCancellationReportsIncompleteRollback(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{Mode: "systemd", OnCalendar: "*-*-* 06:00:00", Timezone: "UTC"}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{
		RemoveErr:  context.Canceled,
		RestoreErr: errors.New("restore failed"),
	}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil || !strings.Contains(err.Error(), "rollback was incomplete") {
		t.Fatalf("incomplete rollback was not reported clearly: %v", err)
	}
}

func TestWizardScheduleSnapshotFailureLeavesConfigurationUntouched(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := existingWizardConfig(root)
	profile := cfg.Profiles["production"]
	profile.Schedule = config.Schedule{Mode: "systemd", OnCalendar: "*-*-* 06:00:00", Timezone: "UTC"}
	cfg.Profiles["production"] = profile
	if err := config.SaveInitial(cfg, config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{SnapshotErr: errors.New("snapshot failed")}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("snapshot failure was reported as success")
	}
	saved, _ := config.Load(paths.Config)
	if saved.Profiles["production"].Schedule.Mode != "systemd" {
		t.Fatal("configuration changed after snapshot failure")
	}
}

func TestWizardScheduleValidationFailureLeavesConfigurationUntouched(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{ValidateErr: errors.New("calendar rejected")}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"4", "", "", "", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err == nil {
		t.Fatal("schedule validation failure was reported as success")
	}
	saved, _ := config.Load(paths.Config)
	if saved.Profiles["production"].Schedule.Mode == "systemd" {
		t.Fatal("configuration changed after schedule validation failure")
	}
}

func TestWizardManualModeRemovesDetectedStaleTimer(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	scheduler := &fakeScheduler{Stale: true}
	script := strings.Join([]string{
		"", "", "", "", "", "", "", "",
		"", "", "", "",
		"1", "", "n",
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("manual cleanup failed: %v\n%s", err, output.String())
	}
	if !scheduler.Removed {
		t.Fatal("stale timer was not removed")
	}
	saved, _ := config.Load(paths.Config)
	if saved.Profiles["production"].Schedule.Mode != "manual" {
		t.Fatal("profile was not saved in manual mode")
	}
}

func TestWizardInvalidCredentialsDoNotWriteConfiguration(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	script := strings.Join([]string{
		"", // profile
		"", // MariaDB
		"2",
		"",
		"",
		"",
		"bad-password",
		"fxserver",
		"n", // do not review
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return errors.New("access denied")
		},
	})
	if err == nil {
		t.Fatal("invalid credentials were accepted")
	}
	if _, statErr := os.Stat(paths.Config); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("configuration was written after failed validation")
	}
}

func TestWizardMissingClientOffersManualDirectoryAndCanCancel(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:    paths,
		Prompter: NewPrompter(strings.NewReader("\n\n\n"), &output),
		DiscoverTools: func(context.Context, config.Database) (mysql.Tools, error) {
			return mysql.Tools{}, errors.New("mariadb-dump not found")
		},
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected clean cancellation after missing tools, got %v", err)
	}
	if !strings.Contains(output.String(), "Client installation directory") {
		t.Fatalf("expected manual directory prompt, got %q", output.String())
	}
	if _, err := os.Stat(paths.Config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wizard saved configuration after cancelling client discovery")
	}
}

func TestWizardRetriesInvalidClientDirectoryBeforeSavingTools(t *testing.T) {
	var output bytes.Buffer
	prompt := NewPrompter(strings.NewReader(
		"\n"+ // MariaDB
			"C:\\missing\\MariaDB\n"+
			"y\n"+ // Retry after rejected directory
			"C:\\Program Files\\MariaDB 12.2\n"+
			"\n"+ // TCP
			"\n"+ // Host
			"\n"+ // Port
			"backup\n"+ // User
			"\n"+ // Empty password
			"y\n"+ // Confirm empty password
			"fxserver\n", // DB name
	), &output)
	attempts := 0
	tools := mysql.Tools{
		Dump:   mysql.ToolInfo{Path: "C:\\Program Files\\MariaDB 12.2\\bin\\mariadb-dump.exe", Version: "12.2"},
		Client: mysql.ToolInfo{Path: "C:\\Program Files\\MariaDB 12.2\\bin\\mariadb.exe", Version: "12.2"},
	}
	profile, _, err := configureDatabase(context.Background(), prompt, Options{
		DiscoverTools: func(context.Context, config.Database) (mysql.Tools, error) {
			return mysql.Tools{}, errors.New("not on PATH")
		},
		DiscoverDirectory: func(_ context.Context, _ config.Database, directory string) (mysql.Tools, error) {
			attempts++
			if directory != "C:\\Program Files\\MariaDB 12.2" {
				return mysql.Tools{}, errors.New("no compatible tools in directory")
			}
			return tools, nil
		},
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
	}, config.Profile{}, "")
	if err != nil {
		t.Fatalf("manual directory resolution failed: %v; output: %s", err, output.String())
	}
	if attempts != 2 || profile.Database.DumpTool != tools.Dump.Path || profile.Database.ClientTool != tools.Client.Path {
		t.Fatalf("manual client paths were not retained: attempts=%d database=%+v", attempts, profile.Database)
	}
}

func TestWizardCancellationPreservesExistingFiles(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(paths.Config)
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(""), &output),
		AllowExisting: true,
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	after, _ := os.ReadFile(paths.Config)
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled wizard changed existing configuration")
	}
}

func TestWizardCancellationAfterReviewPreservesExistingFiles(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := config.SaveInitial(existingWizardConfig(root), config.CredentialFile{Profiles: map[string]config.Credential{
		"production": {Password: "existing"},
	}}, paths); err != nil {
		t.Fatal(err)
	}
	beforeConfig, _ := os.ReadFile(paths.Config)
	beforeCredentials, _ := os.ReadFile(paths.Credentials)
	script := strings.Join([]string{
		"",  // existing profile
		"",  // MariaDB
		"",  // existing TCP
		"",  // host
		"",  // port
		"",  // username
		"",  // database
		"",  // keep existing password
		"",  // backup directory
		"",  // compression
		"",  // retention
		"",  // minimum free
		"1", // manual schedule
		"n", // do not save
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		AllowExisting: true,
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected cancellation, got %v\n%s", err, output.String())
	}
	afterConfig, _ := os.ReadFile(paths.Config)
	afterCredentials, _ := os.ReadFile(paths.Credentials)
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeCredentials, afterCredentials) {
		t.Fatal("cancelled reconfiguration changed existing files")
	}
}

func TestWizardRejectsUnwritableBackupPath(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	notDirectory := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := strings.Join([]string{
		"",
		"",
		"2",
		"",
		"",
		"",
		"secret",
		"fxserver",
		notDirectory,
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected cancellation after invalid backup path, got %v", err)
	}
	if _, statErr := os.Stat(paths.Config); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("configuration was written after invalid backup path")
	}
}

func existingWizardConfig(root string) config.Config {
	return config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: "production",
		Profiles: map[string]config.Profile{
			"production": {
				Database: config.Database{
					Engine: "mysql", Flavor: "mariadb", DumpTool: "auto", ClientTool: "auto",
					Host: "127.0.0.1", Port: 3306, Username: "backup", Database: "fxserver",
				},
				Backup: config.Backup{Directory: filepath.Join(root, "backups"), CompressionLevel: 3},
			},
		},
	}
}

func fakeDiscover(context.Context, config.Database) (mysql.Tools, error) {
	tool := mysql.ToolInfo{Path: "mariadb-dump", Version: "test"}
	client := mysql.ToolInfo{Path: "mariadb", Version: "test"}
	return mysql.Tools{Dump: tool, Client: client}, nil
}

func createFakeBackup(t *testing.T, directory string) (backup.Outcome, error) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return backup.Outcome{}, err
	}
	path, err := archive.NewPath(directory, "production", time.Now().UTC())
	if err != nil {
		return backup.Outcome{}, err
	}
	builder, err := archive.NewBuilder(path, 1)
	if err != nil {
		return backup.Outcome{}, err
	}
	if _, err := io.WriteString(builder, "CREATE TABLE players (id BIGINT);\n"); err != nil {
		return backup.Outcome{}, err
	}
	result, err := builder.Commit(archive.Metadata{
		RunID: "wizard-test", Profile: "production", Database: "fxserver",
		Engine: "mysql", CompressionLevel: 1, DumpTool: "test",
	})
	if err != nil {
		return backup.Outcome{}, err
	}
	return backup.Outcome{Record: history.Record{
		ID:            "wizard-test",
		Profile:       "production",
		Status:        history.StatusSuccess,
		Phase:         "complete",
		Archive:       result.Path,
		ArchiveSHA256: result.Metadata.SHA256,
		ArchiveSize:   result.Metadata.SizeBytes,
		Verified:      true,
	}}, nil
}
