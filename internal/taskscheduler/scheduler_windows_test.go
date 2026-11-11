//go:build windows

package taskscheduler

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

func TestWindowsTaskSchedulerAcceptsGeneratedXML(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "FiveMesh Task Ω & Backup")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fivemesh-backup.exe")
	if err := os.WriteFile(binary, []byte("test binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	scheduler := &Scheduler{
		BinaryPath:  binary,
		CurrentUser: currentUser.Username,
		Paths: config.Paths{
			Config:      filepath.Join(root, "secure config", "config.toml"),
			Credentials: filepath.Join(root, "secure config", "credentials.toml"),
			StateDir:    filepath.Join(root, "state data"),
		},
		Now: func() time.Time {
			return time.Date(2026, 10, 21, 1, 0, 0, 0, time.Local)
		},
	}
	profile := windowsProfile(config.WindowsSchedule{
		Frequency:       config.WindowsFrequencyInterval,
		IntervalMinutes: 360,
		StartTime:       "06:30",
	})
	profile.Schedule.Persistent = true
	data, err := scheduler.GenerateXML("production", profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := (comClient{}).Validate(context.Background(), data); err != nil {
		t.Fatalf("Task Scheduler rejected generated XML: %v\n%s", err, data)
	}
	if strings.Contains(strings.ToLower(string(data)), "password=") {
		t.Fatal("generated task XML contains password material")
	}
}

func TestWindowsTaskSchedulerAcceptsPasswordLogonXML(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	scheduler := testScheduler(t)
	scheduler.CurrentUser = currentUser.Username
	scheduler.CurrentUserSID = currentUser.Uid
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
		LogonMode: config.WindowsLogonPassword,
	})
	data, err := scheduler.GenerateXML("production", profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<LogonType>Password</LogonType>") {
		t.Fatalf("password logon XML was not generated:\n%s", data)
	}
	if err := (comClient{}).Validate(context.Background(), data); err != nil {
		t.Fatalf("Task Scheduler rejected password-logon XML: %v\n%s", err, data)
	}
}

func TestWindowsTaskSchedulerRejectsMalformedXML(t *testing.T) {
	if err := (comClient{}).Validate(context.Background(), []byte(`<Task><Broken></Task>`)); err == nil {
		t.Fatal("malformed task XML was accepted")
	}
}

func TestWindowsTaskSchedulerDistinguishesMissingTask(t *testing.T) {
	name := "FiveMesh Backup nonexistent " + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	state, err := (comClient{}).Get(context.Background(), name)
	if err != nil {
		t.Fatalf("query Task Scheduler root: %v (type %T, value %#v)", err, err, err)
	}
	if state.Exists {
		t.Fatalf("unexpected task exists with name %q", name)
	}
}

func TestWindowsTaskSchedulerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (comClient{}).Get(ctx, "unused"); err == nil {
		t.Fatal("Task Scheduler query ignored cancellation")
	}
}

func TestWindowsTaskXMLQuotesPathsWithoutShellInterpolation(t *testing.T) {
	scheduler := testScheduler(t)
	scheduler.CurrentUser = `DOMAIN\backup user`
	scheduler.BinaryPath = filepath.Join(t.TempDir(), "Program Files", "FiveMesh & Backup", "fivemesh-backup.exe")
	scheduler.Paths.Credentials = filepath.Join(t.TempDir(), "credentials with spaces.toml")
	if err := os.MkdirAll(filepath.Dir(scheduler.BinaryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scheduler.BinaryPath, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := scheduler.GenerateXML("production", windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "FiveMesh &amp; Backup") {
		t.Fatalf("task XML did not escape ampersands:\n%s", text)
	}
	if !strings.Contains(text, "&#34;") {
		t.Fatalf("task XML did not quote arguments with spaces:\n%s", text)
	}
	if strings.Contains(text, "password=") {
		t.Fatal("task arguments contain password material")
	}
}
