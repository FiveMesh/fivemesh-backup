package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/history"
)

func TestInitFromPipedPassword(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	credentialsPath := filepath.Join(root, "credentials.toml")
	stateDir := filepath.Join(root, "state")
	backupDir := filepath.Join(root, "backups")
	args := []string{
		"init",
		"--config", configPath,
		"--credentials", credentialsPath,
		"--state-dir", stateDir,
		"--profile", "production",
		"--user", "backup",
		"--database", "fxserver",
		"--backup-dir", backupDir,
		"--password-stdin",
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader("very-secret-password\n"), &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "very-secret-password") {
		t.Fatal("password was printed by init")
	}
	if !strings.Contains(stdout.String(), "Created profile") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"not-a-command"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
}

func TestConfirmRestoreRequiresExactTarget(t *testing.T) {
	var output bytes.Buffer
	err := confirmRestore(strings.NewReader("wrong\n"), &output, "fxserver_restore")
	if err == nil {
		t.Fatal("mismatched target was accepted")
	}
	output.Reset()
	if err := confirmRestore(strings.NewReader("fxserver_restore\n"), &output, "fxserver_restore"); err != nil {
		t.Fatalf("exact target rejected: %v", err)
	}
}

func TestStatusRecoversAbandonedExecution(t *testing.T) {
	stateDir := t.TempDir()
	store := history.New(stateDir, filepath.Join(stateDir, "logs"))
	if err := store.Append(history.Record{
		ID:        "abandoned",
		Profile:   "production",
		Status:    history.StatusRunning,
		Phase:     "dumping",
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"status", "--state-dir", stateDir},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != exitSuccess {
		t.Fatalf("status exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "interrupted") {
		t.Fatalf("abandoned execution was not shown as interrupted: %s", stdout.String())
	}
}

func TestUninstallConfirmation(t *testing.T) {
	var output bytes.Buffer
	if err := confirmUninstall(strings.NewReader("no\n"), &output); err == nil {
		t.Fatal("invalid uninstall confirmation was accepted")
	}
	if err := confirmUninstall(strings.NewReader("UNINSTALL\n"), &output); err != nil {
		t.Fatalf("valid uninstall confirmation failed: %v", err)
	}
}

func TestConfigureCancellationReturnsInterrupted(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	credentialsPath := filepath.Join(root, "credentials.toml")
	stateDir := filepath.Join(root, "state")
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"configure",
			"--config", configPath,
			"--credentials", credentialsPath,
			"--state-dir", stateDir,
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != exitInterrupted {
		t.Fatalf("exit = %d, want %d; stderr=%s", code, exitInterrupted, stderr.String())
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled configure wrote configuration")
	}
}

func TestScheduleTransitionErrorIncludesRecoveryGuidance(t *testing.T) {
	err := scheduleTransitionError("enable", errors.New("activation failed"), errors.New("restore failed"))
	message := err.Error()
	for _, expected := range []string{"activation failed", "rollback was incomplete", "schedule status", "list-timers"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("error is missing %q: %s", expected, message)
		}
	}
}
