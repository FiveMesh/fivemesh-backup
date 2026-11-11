//go:build linux

package systemd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestExecRunnerPreservesTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := (ExecRunner{}).Run(ctx, "/bin/sleep", "5")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout was not preserved: %v", err)
		}
		var commandFailure *CommandFailure
		if !errors.As(err, &commandFailure) {
			t.Fatalf("timeout did not return CommandFailure: %T", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (ExecRunner{}).Run(ctx, "/bin/sleep", "5")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	})
}

func TestGeneratedUnitsPassSystemdAnalyze(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "fivemesh-backup")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	scheduler := &Scheduler{
		UnitDir:    filepath.Join(root, "units"),
		BinaryPath: binary,
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    stateDir,
		},
	}
	profile := config.Profile{
		Backup: config.Backup{Directory: backupDir},
		Schedule: config.Schedule{
			Mode:       "systemd",
			OnCalendar: "*-*-* 06:00:00",
			Timezone:   "Europe/Brussels",
			Persistent: true,
		},
	}
	serviceData, timerData, err := scheduler.Generate("production", profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scheduler.UnitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service, timer, _ := UnitNames("production")
	servicePath := filepath.Join(scheduler.UnitDir, service)
	timerPath := filepath.Join(scheduler.UnitDir, timer)
	if err := os.WriteFile(servicePath, serviceData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timerPath, timerData, 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(analyze, "verify", servicePath, timerPath).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd-analyze verify failed: %v\n%s", err, output)
	}
}
