//go:build windows

package taskscheduler

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestWindowsTaskSchedulerLifecycleIntegration(t *testing.T) {
	if os.Getenv("FIVEMESH_TEST_TASK_SCHEDULER") != "1" {
		t.Skip("set FIVEMESH_TEST_TASK_SCHEDULER=1 on a disposable Windows account")
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "fivemesh-backup-integration.exe")
	if err := os.WriteFile(binary, []byte("not executed by this lifecycle test"), 0o700); err != nil {
		t.Fatal(err)
	}
	profileName := "integration" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	scheduler := &Scheduler{
		BinaryPath:     binary,
		CurrentUser:    currentUser.Username,
		CurrentUserSID: currentUser.Uid,
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Client: comClient{},
	}
	profile := windowsProfile(config.WindowsSchedule{
		Frequency:       config.WindowsFrequencyInterval,
		IntervalMinutes: 360,
		StartTime:       time.Now().Add(time.Hour).Format("15:04"),
	})
	profile.Schedule.Persistent = true

	before, err := scheduler.Snapshot(context.Background(), profileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Enable(context.Background(), profileName, profile); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := scheduler.Remove(ctx, profileName); err != nil {
			t.Errorf("remove disposable task %q: %v", profileName, err)
		}
	}()

	status := scheduler.Status(context.Background(), profileName, profile)
	if status.Stale || status.Enabled != "true" || status.Next == "" {
		t.Fatalf("registered task is not active and scheduled: %#v", status)
	}
	nextTime, err := time.Parse(time.RFC3339, status.Next)
	if err != nil {
		t.Fatalf("parse next task time %q: %v", status.Next, err)
	}
	if got := nextTime.Format("15:04"); got != profile.Schedule.Windows.StartTime {
		t.Fatalf("next task wall-clock time = %s, want %s (%s)", got, profile.Schedule.Windows.StartTime, status.Next)
	}
	activeSnapshot, err := scheduler.Snapshot(context.Background(), profileName)
	if err != nil {
		t.Fatal(err)
	}
	var active snapshot
	if err := json.Unmarshal(activeSnapshot, &active); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"<LogonType>InteractiveToken</LogonType>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<StartWhenAvailable>true</StartWhenAvailable>",
		"<Interval>PT6H</Interval>",
	} {
		if !strings.Contains(string(active.Current.XML), expected) {
			t.Fatalf("registered task is missing %q: %s", expected, active.Current.XML)
		}
	}
	if err := scheduler.Disable(context.Background(), profileName); err != nil {
		t.Fatal(err)
	}
	disabled := scheduler.Status(context.Background(), profileName, profile)
	if !disabled.Stale || disabled.Enabled != "false" {
		t.Fatalf("disabled task was not detected safely: %#v", disabled)
	}
	if err := scheduler.Restore(context.Background(), profileName, activeSnapshot); err != nil {
		t.Fatal(err)
	}
	restored := scheduler.Status(context.Background(), profileName, profile)
	if restored.Stale || restored.Enabled != "true" || restored.Next == "" {
		t.Fatalf("active task snapshot was not restored: %#v", restored)
	}
	if err := scheduler.Remove(context.Background(), profileName); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Restore(context.Background(), profileName, before); err != nil {
		t.Fatal(err)
	}
	missing := scheduler.Status(context.Background(), profileName, config.Profile{
		Schedule: config.Schedule{Mode: config.ScheduleModeManual},
	})
	if missing.Stale {
		t.Fatalf("task existed after restoring the pre-registration snapshot: %#v", missing)
	}
}

func TestWindowsTaskSchedulerManualRunConfirmation(t *testing.T) {
	if os.Getenv("FIVEMESH_TEST_TASK_SCHEDULER") != "1" {
		t.Skip("set FIVEMESH_TEST_TASK_SCHEDULER=1 on a disposable Windows account")
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	scheduler := &Scheduler{
		BinaryPath:     filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe"),
		CurrentUser:    currentUser.Username,
		CurrentUserSID: currentUser.Uid,
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Client:                 comClient{},
		RunConfirmationTimeout: 10 * time.Second,
	}
	profileName := "manualrun" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: time.Now().Add(time.Hour).Format("15:04"),
	})
	if _, err := scheduler.Enable(context.Background(), profileName, profile); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := scheduler.Remove(ctx, profileName); err != nil {
			t.Errorf("remove disposable manual-run task %q: %v", profileName, err)
		}
	}()
	if err := scheduler.Run(context.Background(), profileName); err != nil {
		t.Fatalf("Task Scheduler accepted but did not confirm a manual task start: %v", err)
	}
}
