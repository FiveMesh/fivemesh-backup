package taskscheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

func backgroundProfile() config.Profile {
	return windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
		LogonMode: config.WindowsLogonBackground,
	})
}

func TestBackgroundLogonGeneratesS4UTaskWithoutPassword(t *testing.T) {
	scheduler := testScheduler(t)
	data, err := scheduler.GenerateXML("production", backgroundProfile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<LogonType>S4U</LogonType>") ||
		!strings.Contains(string(data), "<RunLevel>LeastPrivilege</RunLevel>") {
		t.Fatalf("background task XML is not S4U least privilege:\n%s", data)
	}
	if taskLogonMode(data) != "background" {
		t.Fatalf("taskLogonMode = %q", taskLogonMode(data))
	}
	name := mustTaskName(t, scheduler, "production")
	if !scheduler.isManagedTask(data, name, "production", false) {
		t.Fatal("generated background task is not recognized as managed")
	}
}

func TestBackgroundLogonRegistersS4UWithOwnerDescriptor(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{state: taskState{Next: "2026-10-22T06:00:00+02:00"}}
	scheduler.Client = client
	if _, err := scheduler.Enable(context.Background(), "production", backgroundProfile()); err != nil {
		t.Fatal(err)
	}
	registration := client.registration
	if registration.LogonType != taskLogonS4U || registration.Password != "" ||
		registration.UserID != scheduler.CurrentUser {
		t.Fatalf("unexpected background registration: %#v", registration)
	}
	want := "D:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FA;;;S-1-5-21-1000)"
	if registration.SecurityDescriptor != want {
		t.Fatalf("security descriptor = %q, want %q", registration.SecurityDescriptor, want)
	}
}

func TestBackgroundLogonNeverNeedsWindowsPassword(t *testing.T) {
	scheduler := testScheduler(t)
	data, err := scheduler.GenerateXML("production", backgroundProfile())
	if err != nil {
		t.Fatal(err)
	}
	state := taskState{Exists: true, Enabled: true, XML: data}
	scheduler.Client = &fakeClient{state: state}
	snapshot, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.SnapshotRequiresPassword(snapshot) {
		t.Fatal("snapshot of a background task must not require a Windows password")
	}
	if _, err := scheduler.registrationForTask(data, schedule.LogonSecret{}); err != nil {
		t.Fatalf("background registration must not need a password: %v", err)
	}
}

func TestBackgroundLogonRejectsNetworkBackupDirectory(t *testing.T) {
	scheduler := testScheduler(t)
	for _, directory := range []string{`\\nas\backups`, "//nas/backups"} {
		profile := backgroundProfile()
		profile.Backup.Directory = directory
		err := scheduler.Validate(context.Background(), "production", profile)
		if err == nil || !strings.Contains(err.Error(), "network location") {
			t.Fatalf("Validate(%q) error = %v", directory, err)
		}
		profile.Schedule.Windows.LogonMode = config.WindowsLogonPassword
		if err := scheduler.validateProfile("production", profile); err != nil {
			t.Fatalf("password mode must allow network directories: %v", err)
		}
	}
}

func TestProfileFromTaskReadsGeneratedDescription(t *testing.T) {
	scheduler := testScheduler(t)
	data, err := scheduler.GenerateXML("my.profile-1", backgroundProfile())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profileFromTask(data)
	if err != nil || profile != "my.profile-1" {
		t.Fatalf("profileFromTask = %q, %v", profile, err)
	}
	if _, err := profileFromTask([]byte(`<Task><RegistrationInfo><Description>other</Description></RegistrationInfo></Task>`)); err == nil {
		t.Fatal("foreign task description was accepted")
	}
}
