package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Version:        CurrentVersion,
		DefaultProfile: "production",
		Profiles: map[string]Profile{
			"production": {
				Database: Database{
					Engine:            "mysql",
					DumpTool:          "auto",
					ClientTool:        "auto",
					Host:              "127.0.0.1",
					Port:              3306,
					Username:          "backup",
					Database:          "fxserver",
					SingleTransaction: true,
				},
				Backup: Backup{
					Directory:        filepath.Join("testdata", "backups"),
					CompressionLevel: 3,
				},
				Retention: Retention{KeepLast: 7},
			},
		},
	}
}

func TestValidateConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateRejectsUnsafeDatabaseArgument(t *testing.T) {
	cfg := validConfig()
	profile := cfg.Profiles["production"]
	profile.Database.Database = "--all-databases"
	cfg.Profiles["production"] = profile
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "start with") {
		t.Fatalf("expected unsafe database name rejection, got %v", err)
	}
}

func TestValidateRejectsInvalidTimeout(t *testing.T) {
	cfg := validConfig()
	profile := cfg.Profiles["production"]
	profile.Backup.Timeout = "-1h"
	cfg.Profiles["production"] = profile
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid timeout rejection")
	}
}

func TestValidateRejectsCredentialOverrideInExtraArgs(t *testing.T) {
	cfg := validConfig()
	profile := cfg.Profiles["production"]
	profile.Database.ExtraDumpArgs = []string{"--password=secret"}
	cfg.Profiles["production"] = profile
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not override credentials") {
		t.Fatalf("expected credential override rejection, got %v", err)
	}
}

func TestValidateSystemdSchedule(t *testing.T) {
	cfg := validConfig()
	profile := cfg.Profiles["production"]
	profile.Schedule = Schedule{
		Mode:       "systemd",
		OnCalendar: "*-*-* 06:00:00",
		Timezone:   "Europe/Brussels",
		Persistent: true,
	}
	cfg.Profiles["production"] = profile
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	profile.Schedule.Timezone = "Not/AZone"
	cfg.Profiles["production"] = profile
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}

func TestValidateWindowsSchedules(t *testing.T) {
	cases := []struct {
		name     string
		schedule Schedule
		wantErr  string
	}{
		{
			name: "daily",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "06:00",
				},
			},
		},
		{
			name: "six hours",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency:       WindowsFrequencyInterval,
					IntervalMinutes: 360,
					StartTime:       "06:30",
				},
			},
		},
		{
			name: "custom three hours",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency:       WindowsFrequencyInterval,
					IntervalMinutes: 180,
					StartTime:       "23:45",
				},
			},
		},
		{
			name: "password logon",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "06:00",
					LogonMode: WindowsLogonPassword,
				},
			},
		},
		{
			name: "non-divisor interval",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency:       WindowsFrequencyInterval,
					IntervalMinutes: 100,
					StartTime:       "06:00",
				},
			},
			wantErr: "divide evenly",
		},
		{
			name: "too frequent",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency:       WindowsFrequencyInterval,
					IntervalMinutes: 5,
					StartTime:       "06:00",
				},
			},
			wantErr: "between 15 and 1440",
		},
		{
			name: "invalid clock",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "24:00",
				},
			},
			wantErr: "valid 24-hour time",
		},
		{
			name: "systemd calendar is rejected",
			schedule: Schedule{
				Mode:       ScheduleModeWindows,
				OnCalendar: "*-*-* 06:00:00",
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "06:00",
				},
			},
			wantErr: "on_calendar is not valid",
		},
		{
			name: "non-local timezone is rejected",
			schedule: Schedule{
				Mode:     ScheduleModeWindows,
				Timezone: "Europe/Brussels",
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "06:00",
				},
			},
			wantErr: "timezone must be Local",
		},
		{
			name: "unsupported S4U logon is rejected",
			schedule: Schedule{
				Mode: ScheduleModeWindows,
				Windows: WindowsSchedule{
					Frequency: WindowsFrequencyDaily,
					StartTime: "06:00",
					LogonMode: "s4u",
				},
			},
			wantErr: "logon_mode must be background, interactive, or password",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := validConfig()
			profile := cfg.Profiles["production"]
			profile.Schedule = testCase.schedule
			cfg.Profiles["production"] = profile
			err := cfg.Validate()
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("valid schedule rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestSaveInitialCreatesRestrictedCredentials(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups"))
	if err := SaveInitial(cfg, CredentialFile{
		Profiles: map[string]Credential{"production": {Password: "secret"}},
	}, paths); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	loaded, err := Load(paths.Config)
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if loaded.DefaultProfile != "production" {
		t.Fatalf("unexpected default profile %q", loaded.DefaultProfile)
	}
	password, err := LoadCredential(paths.Credentials, "production")
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if password != "secret" {
		t.Fatal("password did not round-trip")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(paths.Credentials)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials mode = %04o, want 0600", info.Mode().Perm())
		}
	}
}

func TestWindowsScheduleRoundTripsWithoutSchemaBreakage(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	profile := cfg.Profiles["production"]
	profile.Backup.Directory = filepath.Join(root, "backups")
	profile.Schedule = Schedule{
		Mode:       ScheduleModeWindows,
		Persistent: true,
		Windows: WindowsSchedule{
			Frequency:       WindowsFrequencyInterval,
			IntervalMinutes: 720,
			StartTime:       "23:45",
			LogonMode:       WindowsLogonPassword,
		},
	}
	cfg.Profiles["production"] = profile
	if err := SaveInitial(cfg, CredentialFile{
		Profiles: map[string]Credential{"production": {Password: "secret"}},
	}, paths); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Profiles["production"].Schedule
	if got.Mode != ScheduleModeWindows || got.OnCalendar != "" ||
		got.Timezone != "" || got.Windows != profile.Schedule.Windows ||
		!got.Persistent {
		t.Fatalf("Windows schedule did not round-trip: %#v", got)
	}
}

func TestSaveReconfiguredPreservesOtherProfiles(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Profiles["staging"] = cfg.Profiles["production"]
	cfg.Profiles["production"] = withBackupDirectory(cfg.Profiles["production"], filepath.Join(root, "backups-production"))
	cfg.Profiles["staging"] = withBackupDirectory(cfg.Profiles["staging"], filepath.Join(root, "backups-staging"))
	credentials := CredentialFile{Profiles: map[string]Credential{
		"production": {Password: "one"},
		"staging":    {Password: "two"},
	}}
	if err := SaveInitial(cfg, credentials, paths); err != nil {
		t.Fatal(err)
	}
	updated := cfg
	profile := updated.Profiles["production"]
	profile.Description = "updated"
	updated.Profiles["production"] = profile
	credentials.Profiles["production"] = Credential{Password: "new-secret"}
	if err := SaveReconfigured(updated, credentials, paths); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles["production"].Description != "updated" || loaded.Profiles["staging"].Description != "" {
		t.Fatalf("profiles were not preserved: %#v", loaded.Profiles)
	}
	password, err := LoadCredential(paths.Credentials, "production")
	if err != nil || password != "new-secret" {
		t.Fatalf("credential update failed: password=%q err=%v", password, err)
	}
}

func withBackupDirectory(profile Profile, directory string) Profile {
	profile.Backup.Directory = directory
	return profile
}
