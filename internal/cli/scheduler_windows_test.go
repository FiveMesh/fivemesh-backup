//go:build windows

package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestWindowsSchedulerFactoryUsesTaskScheduler(t *testing.T) {
	root := t.TempDir()
	scheduler, err := buildScheduler(config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.Mode() != config.ScheduleModeWindows {
		t.Fatalf("scheduler mode = %q, want %q", scheduler.Mode(), config.ScheduleModeWindows)
	}
}

func TestWindowsScheduleStatusReportsUnknownRatherThanDisabled(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: "windowstest",
		Profiles: map[string]config.Profile{
			"windowstest": {
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
					Directory:        filepath.Join(root, "backups"),
					CompressionLevel: 3,
				},
				Schedule: config.Schedule{
					Mode: config.ScheduleModeWindows,
					Windows: config.WindowsSchedule{
						Frequency: config.WindowsFrequencyDaily,
						StartTime: "06:00",
					},
				},
			},
		},
	}
	if err := config.SaveInitial(cfg, config.CredentialFile{
		Profiles: map[string]config.Credential{"windowstest": {Password: "separate-secret"}},
	}, paths); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"schedule", "status",
		"--profile", "windowstest",
		"--config", paths.Config,
		"--credentials", paths.Credentials,
		"--state-dir", paths.StateDir,
	}, strings.NewReader(""), &stdout, &stderr)
	if code == exitSuccess {
		t.Fatal("missing Windows task was reported as matching configuration")
	}
	combined := stdout.String() + stderr.String()
	for _, expected := range []string{
		"windowstest: windows",
		"enabled=false",
		"active=not-found",
		"runs       unknown",
		"task does not exist",
	} {
		if !strings.Contains(combined, expected) {
			t.Fatalf("schedule status is missing %q:\n%s", expected, combined)
		}
	}
}
