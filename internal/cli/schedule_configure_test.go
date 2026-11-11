package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

type scheduleTestManager struct {
	mode           string
	snapshot       []byte
	enabled        bool
	enabledProfile config.Profile
	enabledSecret  schedule.LogonSecret
	restored       bool
	restoreSecret  schedule.LogonSecret
}

func (m *scheduleTestManager) Mode() string             { return m.mode }
func (m *scheduleTestManager) Manages(mode string) bool { return mode == m.mode }
func (m *scheduleTestManager) Validate(context.Context, string, config.Profile) error {
	return nil
}
func (m *scheduleTestManager) Enable(
	_ context.Context,
	_ string,
	profile config.Profile,
	secrets ...schedule.LogonSecret,
) (string, error) {
	m.enabled = true
	m.enabledProfile = profile
	if len(secrets) > 0 {
		m.enabledSecret = secrets[0]
	}
	return "2026-10-22T06:00:00+02:00", nil
}
func (m *scheduleTestManager) Disable(context.Context, string) error { return nil }
func (m *scheduleTestManager) Remove(context.Context, string) error  { return nil }
func (m *scheduleTestManager) Snapshot(context.Context, string) ([]byte, error) {
	return append([]byte(nil), m.snapshot...), nil
}
func (m *scheduleTestManager) SnapshotRequiresPassword([]byte) bool { return false }
func (m *scheduleTestManager) Restore(
	_ context.Context,
	_ string,
	_ []byte,
	secrets ...schedule.LogonSecret,
) error {
	m.restored = true
	if len(secrets) > 0 {
		m.restoreSecret = secrets[0]
	}
	return nil
}
func (m *scheduleTestManager) Inspect(context.Context, string, config.Profile) (bool, string, error) {
	return false, "", nil
}
func (m *scheduleTestManager) Status(context.Context, string, config.Profile) schedule.Status {
	return schedule.Status{}
}
func (m *scheduleTestManager) Next(context.Context, string, config.Profile) (string, error) {
	return "", nil
}
func (m *scheduleTestManager) Run(context.Context, string) error { return nil }
func (m *scheduleTestManager) Logs(context.Context, string, int) ([]byte, error) {
	return nil, nil
}

func TestConfigureWindowsProfilePresetsAndCompatibility(t *testing.T) {
	profile := validScheduleCLIProfile()
	profile.Schedule = config.Schedule{
		Mode: config.ScheduleModeWindows,
		Windows: config.WindowsSchedule{
			Frequency:       config.WindowsFrequencyInterval,
			IntervalMinutes: 360,
			StartTime:       "06:00",
			LogonMode:       config.WindowsLogonInteractive,
		},
	}
	originalDirectory := profile.Backup.Directory
	originalUsername := profile.Database.Username
	configured, err := configureWindowsProfile(profile, "6h", 0, "06:00", "password", "true")
	if err != nil {
		t.Fatal(err)
	}
	if configured.Schedule.Mode != config.ScheduleModeWindows ||
		configured.Schedule.Windows.Frequency != config.WindowsFrequencyInterval ||
		configured.Schedule.Windows.IntervalMinutes != 360 ||
		configured.Schedule.Windows.StartTime != "06:00" ||
		configured.Schedule.Windows.EffectiveLogonMode() != config.WindowsLogonPassword ||
		!configured.Schedule.Persistent {
		t.Fatalf("unexpected configured schedule: %#v", configured.Schedule)
	}
	if configured.Backup.Directory != originalDirectory || configured.Database.Username != originalUsername {
		t.Fatal("non-schedule profile fields changed")
	}
}

func TestConfigureWindowsProfileCustomAndValidation(t *testing.T) {
	profile := validScheduleCLIProfile()
	configured, err := configureWindowsProfile(profile, "custom", 180, "23:45", "interactive", "false")
	if err != nil {
		t.Fatal(err)
	}
	if configured.Schedule.Windows.IntervalMinutes != 180 || configured.Schedule.Persistent {
		t.Fatalf("unexpected custom schedule: %#v", configured.Schedule)
	}
	cases := []struct {
		name       string
		frequency  string
		interval   int
		startTime  string
		logonMode  string
		persistent string
		want       string
	}{
		{name: "missing frequency", startTime: "06:00", logonMode: "interactive", persistent: "true", want: "--frequency"},
		{name: "bad time", frequency: "daily", startTime: "24:00", logonMode: "interactive", persistent: "true", want: "24-hour time"},
		{name: "bad logon", frequency: "daily", startTime: "06:00", logonMode: "s4u", persistent: "true", want: "--logon-mode"},
		{name: "bad persistent", frequency: "daily", startTime: "06:00", logonMode: "interactive", persistent: "sometimes", want: "--persistent"},
		{name: "custom without interval", frequency: "custom", startTime: "06:00", logonMode: "interactive", persistent: "true", want: "--interval-minutes"},
		{name: "custom non-divisor", frequency: "custom", interval: 100, startTime: "06:00", logonMode: "interactive", persistent: "true", want: "divide evenly"},
		{name: "interval on preset", frequency: "6h", interval: 180, startTime: "06:00", logonMode: "interactive", persistent: "true", want: "only valid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := configureWindowsProfile(
				profile,
				testCase.frequency,
				testCase.interval,
				testCase.startTime,
				testCase.logonMode,
				testCase.persistent,
			)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestScheduleConfigureCommandUpdatesExistingProfile(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: "production",
		Profiles:       map[string]config.Profile{"production": validScheduleCLIProfile()},
	}
	if err := config.SaveInitial(cfg, config.CredentialFile{
		Profiles: map[string]config.Credential{"production": {Password: "database-secret"}},
	}, paths); err != nil {
		t.Fatal(err)
	}
	manager := &scheduleTestManager{
		mode:     config.ScheduleModeWindows,
		snapshot: []byte("prior-task-state"),
	}
	originalBuilder := scheduleBuilder
	defer func() { scheduleBuilder = originalBuilder }()
	scheduleBuilder = func(config.Paths) (schedule.Manager, error) {
		return manager, nil
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"schedule", "configure",
		"--profile", "production",
		"--frequency", "6h",
		"--start-time", "06:00",
		"--logon-mode", "interactive",
		"--persistent", "true",
		"--config", paths.Config,
		"--credentials", paths.Credentials,
		"--state-dir", paths.StateDir,
	}, strings.NewReader(""), &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	loaded, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Profiles["production"].Schedule
	if got.Mode != config.ScheduleModeWindows ||
		got.Windows.IntervalMinutes != 360 ||
		got.Windows.StartTime != "06:00" ||
		got.Windows.EffectiveLogonMode() != config.WindowsLogonInteractive ||
		!got.Persistent {
		t.Fatalf("unexpected persisted schedule: %#v", got)
	}
	if !manager.enabled || manager.enabledProfile.Schedule.Windows.IntervalMinutes != 360 {
		t.Fatal("native scheduler was not updated")
	}
}

func TestEnableAndSaveScheduleRollsBackAfterConfigFailure(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config-target")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := validScheduleCLIProfile()
	configured, err := configureWindowsProfile(profile, "12h", 0, "06:00", "password", "true")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: "production",
		Profiles:       map[string]config.Profile{"production": profile},
	}
	manager := &scheduleTestManager{
		mode:     config.ScheduleModeWindows,
		snapshot: []byte("prior-task-state"),
	}
	secret := schedule.LogonSecret{WindowsPassword: "ephemeral-secret"}
	_, err = enableAndSaveSchedule(
		context.Background(),
		manager,
		cfg,
		configPath,
		"production",
		configured,
		manager.snapshot,
		secret,
		"configure schedule",
	)
	if err == nil {
		t.Fatal("configuration failure was not reported")
	}
	if !manager.enabled || !manager.restored {
		t.Fatalf("task transition was not rolled back: enabled=%t restored=%t", manager.enabled, manager.restored)
	}
	if manager.enabledProfile.Schedule.Windows.EffectiveLogonMode() != config.WindowsLogonPassword ||
		manager.restoreSecret.WindowsPassword != "ephemeral-secret" {
		t.Fatal("schedule migration or password handoff was not preserved")
	}
}

func validScheduleCLIProfile() config.Profile {
	return config.Profile{
		Database: config.Database{
			Engine:     "mysql",
			DumpTool:   "auto",
			ClientTool: "auto",
			Host:       "127.0.0.1",
			Port:       3306,
			Username:   "backup",
			Database:   "fxserver",
		},
		Backup: config.Backup{
			Directory:        filepath.Join("testdata", "backups"),
			CompressionLevel: 3,
		},
		Retention: config.Retention{KeepLast: 7},
	}
}
