package taskscheduler

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

type fakeClient struct {
	state          taskState
	validateErr    error
	getErr         error
	registerErr    error
	setEnabledErr  error
	removeErr      error
	runErr         error
	ignoreDisable  bool
	ignoreRemove   bool
	registerData   []byte
	registration   taskRegistration
	validateData   []byte
	setEnabledCall bool
	removeCalled   bool
	runCalled      bool
}

type mappedClient struct {
	states        map[string]taskState
	registrations map[string]taskRegistration
	mutations     []string
	validateErr   error
}

func (c *mappedClient) Validate(ctx context.Context, _ []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.validateErr
}

func (c *mappedClient) Get(ctx context.Context, name string) (taskState, error) {
	if err := ctx.Err(); err != nil {
		return taskState{}, err
	}
	state := c.states[name]
	state.XML = append([]byte(nil), state.XML...)
	return state, nil
}

func (c *mappedClient) Register(ctx context.Context, name string, data []byte, registration taskRegistration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mutations = append(c.mutations, "register:"+name)
	if c.registrations == nil {
		c.registrations = make(map[string]taskRegistration)
	}
	c.registrations[name] = registration
	c.states[name] = taskState{
		Exists:  true,
		Enabled: true,
		State:   taskStateReady,
		Next:    "2026-10-22T06:00:00+02:00",
		XML:     append([]byte(nil), data...),
	}
	return nil
}

func (c *mappedClient) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mutations = append(c.mutations, "enabled:"+name)
	state := c.states[name]
	state.Enabled = enabled
	c.states[name] = state
	return nil
}

func (c *mappedClient) Remove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mutations = append(c.mutations, "remove:"+name)
	delete(c.states, name)
	return nil
}

func (c *mappedClient) Run(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mutations = append(c.mutations, "run:"+name)
	return nil
}

func (c *fakeClient) Validate(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.validateData = append([]byte(nil), data...)
	return c.validateErr
}

func (c *fakeClient) Get(ctx context.Context, _ string) (taskState, error) {
	if err := ctx.Err(); err != nil {
		return taskState{}, err
	}
	return c.state, c.getErr
}

func (c *fakeClient) Register(ctx context.Context, _ string, data []byte, registration taskRegistration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.registerErr != nil {
		return c.registerErr
	}
	c.registerData = append([]byte(nil), data...)
	c.registration = registration
	c.state.Exists = true
	c.state.Enabled = true
	c.state.State = taskStateReady
	c.state.XML = append([]byte(nil), data...)
	return nil
}

func (c *fakeClient) SetEnabled(ctx context.Context, _ string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.setEnabledCall = true
	if c.setEnabledErr != nil {
		return c.setEnabledErr
	}
	if !c.ignoreDisable {
		c.state.Enabled = enabled
	}
	return nil
}

func (c *fakeClient) Remove(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.removeErr != nil {
		return c.removeErr
	}
	c.removeCalled = true
	if !c.ignoreRemove {
		c.state = taskState{}
	}
	return nil
}

func (c *fakeClient) Run(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.runCalled = true
	return c.runErr
}

func TestGenerateXMLPresetsAndSecuritySettings(t *testing.T) {
	cases := []struct {
		name       string
		schedule   config.WindowsSchedule
		repetition string
	}{
		{
			name: "daily",
			schedule: config.WindowsSchedule{
				Frequency: config.WindowsFrequencyDaily,
				StartTime: "06:00",
			},
		},
		{
			name: "six hours",
			schedule: config.WindowsSchedule{
				Frequency:       config.WindowsFrequencyInterval,
				IntervalMinutes: 360,
				StartTime:       "06:30",
			},
			repetition: "<Interval>PT6H</Interval>",
		},
		{
			name: "twelve hours",
			schedule: config.WindowsSchedule{
				Frequency:       config.WindowsFrequencyInterval,
				IntervalMinutes: 720,
				StartTime:       "23:45",
			},
			repetition: "<Interval>PT12H</Interval>",
		},
		{
			name: "custom three hours",
			schedule: config.WindowsSchedule{
				Frequency:       config.WindowsFrequencyInterval,
				IntervalMinutes: 180,
				StartTime:       "12:15",
			},
			repetition: "<Interval>PT3H</Interval>",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scheduler := testScheduler(t)
			profile := windowsProfile(testCase.schedule)
			profile.Schedule.Persistent = true
			data, err := scheduler.GenerateXML("production & test", profile)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, expected := range []string{
				"<LogonType>InteractiveToken</LogonType>",
				"<RunLevel>LeastPrivilege</RunLevel>",
				"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
				"<StartWhenAvailable>true</StartWhenAvailable>",
				"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
				"--credentials",
				"FiveMesh Backup",
			} {
				if !strings.Contains(text, expected) {
					t.Fatalf("generated XML is missing %q:\n%s", expected, text)
				}
			}
			if testCase.repetition == "" {
				if strings.Contains(text, "<Repetition>") {
					t.Fatalf("daily task unexpectedly has repetition:\n%s", text)
				}
			} else if !strings.Contains(text, testCase.repetition) {
				t.Fatalf("generated XML is missing repetition %q:\n%s", testCase.repetition, text)
			}
			if strings.Contains(strings.ToLower(text), "password=") {
				t.Fatalf("task definition contains password material:\n%s", text)
			}
			xmlBody := data
			if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
				xmlBody = data[newline+1:]
			}
			var root struct {
				XMLName xml.Name
			}
			if err := xml.Unmarshal(xmlBody, &root); err != nil {
				t.Fatalf("generated XML is malformed: %v", err)
			}
			if root.XMLName.Local != "Task" {
				t.Fatalf("root element = %q, want Task", root.XMLName.Local)
			}
		})
	}
}

func TestPasswordLogonRequiresEphemeralSecret(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{state: taskState{Next: "2026-10-22T06:00:00+02:00"}}
	scheduler.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
		LogonMode: config.WindowsLogonPassword,
	})
	if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil ||
		!strings.Contains(err.Error(), "Windows password is required") {
		t.Fatalf("password mode accepted an empty secret: %v", err)
	}
	if len(client.registerData) != 0 {
		t.Fatal("passwordless registration reached Task Scheduler")
	}
	const windowsPassword = "not-in-task-xml-or-logs"
	if _, err := scheduler.Enable(context.Background(), "production", profile, schedule.LogonSecret{
		WindowsPassword: windowsPassword,
	}); err != nil {
		t.Fatal(err)
	}
	if client.registration.LogonType != taskLogonPassword ||
		client.registration.UserID != scheduler.CurrentUser ||
		client.registration.Password != windowsPassword {
		t.Fatalf("unexpected Task Scheduler registration: %#v", client.registration)
	}
	if strings.Contains(string(client.registerData), windowsPassword) {
		t.Fatal("Windows password was written into task XML")
	}
	if !strings.Contains(string(client.registerData), "<LogonType>Password</LogonType>") {
		t.Fatal("password logon mode was not encoded in task XML")
	}
}

func TestInteractiveLogonRegistersWithoutPassword(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{state: taskState{Next: "2026-10-22T06:00:00+02:00"}}
	scheduler.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := scheduler.Enable(context.Background(), "production", profile); err != nil {
		t.Fatal(err)
	}
	if client.registration.LogonType != taskLogonInteractiveToken || client.registration.Password != "" {
		t.Fatalf("unexpected interactive registration: %#v", client.registration)
	}
}

func TestTaskNamesAreStableAndProfileIsolated(t *testing.T) {
	scheduler := testScheduler(t)
	first, err := scheduler.taskName("production")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.taskName("production")
	if err != nil {
		t.Fatal(err)
	}
	other, err := scheduler.taskName("staging")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other {
		t.Fatalf("unexpected task names: first=%q second=%q other=%q", first, second, other)
	}
	if strings.ContainsAny(first, `/\`) {
		t.Fatalf("task name unexpectedly creates a folder path: %q", first)
	}
}

func TestTaskIdentityIncludesInstallationAndAction(t *testing.T) {
	first := testScheduler(t)
	second := *first
	second.Paths.Config = filepath.Join(filepath.Dir(first.Paths.Config), "second-home", "config.toml")
	second.Paths.Credentials = filepath.Join(filepath.Dir(second.Paths.Config), "credentials.toml")
	second.Paths.StateDir = filepath.Join(filepath.Dir(second.Paths.Config), "state")

	firstName := mustTaskName(t, first, "production")
	secondName := mustTaskName(t, &second, "production")
	if firstName == secondName {
		t.Fatal("independent configuration homes generated the same task name")
	}
	canonicalAlias := *first
	canonicalAlias.Paths.Config = filepath.Join(filepath.Dir(first.Paths.Config), "nested", "..", filepath.Base(first.Paths.Config))
	if aliasName := mustTaskName(t, &canonicalAlias, "production"); aliasName != firstName {
		t.Fatal("canonical path aliases generated different task names")
	}
	firstState := managedState(t, first, firstName, "production", false)
	forged := string(firstState.XML)
	forged = strings.Replace(forged, first.taskDescription("production", false), second.taskDescription("production", false), 1)
	forged = strings.Replace(forged, `<URI>\`+firstName+`</URI>`, `<URI>\`+secondName+`</URI>`, 1)
	if second.isManagedTask([]byte(forged), secondName, "production", false) {
		t.Fatal("forged description and URI hid mismatched installation actions")
	}
}

func TestIndependentInstallationsCannotMutateEachOther(t *testing.T) {
	first := testScheduler(t)
	second := *first
	second.Paths.Config = filepath.Join(filepath.Dir(first.Paths.Config), "second-home", "config.toml")
	second.Paths.Credentials = filepath.Join(filepath.Dir(second.Paths.Config), "credentials.toml")
	second.Paths.StateDir = filepath.Join(filepath.Dir(second.Paths.Config), "state")
	client := &mappedClient{states: make(map[string]taskState)}
	first.Client = client
	second.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := first.Enable(context.Background(), "production", profile); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Enable(context.Background(), "production", profile); err != nil {
		t.Fatal(err)
	}
	firstName := mustTaskName(t, first, "production")
	secondName := mustTaskName(t, &second, "production")
	if err := first.Disable(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if !client.states[secondName].Exists || !client.states[secondName].Enabled {
		t.Fatal("disabling one installation changed another installation's task")
	}
	if err := first.Remove(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if client.states[firstName].Exists {
		t.Fatal("first installation task remains after removal")
	}
	if !client.states[secondName].Exists || !client.states[secondName].Enabled {
		t.Fatal("removing one installation changed another installation's task")
	}
}

func TestEnableAndDisableVerifyTaskState(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{state: taskState{Next: "2026-10-22T06:00:00+02:00"}}
	scheduler.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	next, err := scheduler.Enable(context.Background(), "production", profile)
	if err != nil {
		t.Fatal(err)
	}
	if next != "2026-10-22T06:00:00+02:00" {
		t.Fatalf("next execution = %q", next)
	}
	if !strings.Contains(string(client.registerData), "<StartWhenAvailable>false</StartWhenAvailable>") {
		t.Fatal("task XML was not registered")
	}
	if err := scheduler.Disable(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if client.state.Enabled {
		t.Fatal("task was not disabled")
	}
}

func TestDisableFailsClosedWhenStateCannotBeConfirmed(t *testing.T) {
	scheduler := testScheduler(t)
	name, err := scheduler.taskName("production")
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{
		state:         managedState(t, scheduler, name, "production", false),
		ignoreDisable: true,
	}
	scheduler.Client = client
	err = scheduler.Disable(context.Background(), "production")
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("unconfirmed disable was treated as success: %v", err)
	}
}

func TestForeignTaskCollisionIsNeverModified(t *testing.T) {
	scheduler := testScheduler(t)
	name, err := scheduler.taskName("production")
	if err != nil {
		t.Fatal(err)
	}
	foreignXML := []byte(`<?xml version="1.0"?><Task><RegistrationInfo><Description>Other product</Description><URI>\` + name + `</URI></RegistrationInfo></Task>`)
	client := &fakeClient{
		state: taskState{
			Exists:  true,
			Enabled: true,
			State:   taskStateReady,
			XML:     foreignXML,
		},
	}
	scheduler.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil ||
		!strings.Contains(err.Error(), "unrelated Windows task") {
		t.Fatalf("Enable accepted foreign task collision: %v", err)
	}
	if len(client.registerData) != 0 {
		t.Fatal("foreign task was overwritten")
	}
	if err := scheduler.Disable(context.Background(), "production"); err == nil ||
		!strings.Contains(err.Error(), "unrelated Windows task") {
		t.Fatalf("Disable accepted foreign task collision: %v", err)
	}
	if err := scheduler.Remove(context.Background(), "production"); err == nil ||
		!strings.Contains(err.Error(), "unrelated Windows task") {
		t.Fatalf("Remove accepted foreign task collision: %v", err)
	}
	status := scheduler.Status(context.Background(), "production", profile)
	if !status.Stale || !strings.Contains(status.UnitError, "not owned") {
		t.Fatalf("foreign task collision was not visible in status: %#v", status)
	}
}

func TestSnapshotFailsWhenTaskSchedulerStateIsUnknown(t *testing.T) {
	scheduler := testScheduler(t)
	scheduler.Client = &fakeClient{getErr: errors.New("D-Bus equivalent unavailable")}
	if _, err := scheduler.Snapshot(context.Background(), "production"); err == nil {
		t.Fatal("snapshot succeeded while scheduler state was unknown")
	}
}

func TestRestoreDoesNotDeleteForeignTaskAfterAbsentSnapshot(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{}
	scheduler.Client = client
	saved, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	name := mustTaskName(t, scheduler, "production")
	foreign := managedState(t, scheduler, name, "production", false)
	foreign.XML = alterTaskCommand(t, foreign.XML, `C:\Other Product\other.exe`)
	client.state = foreign
	if err := scheduler.Restore(context.Background(), "production", saved); err == nil ||
		!strings.Contains(err.Error(), "unrelated task now occupies") {
		t.Fatalf("rollback deleted or ignored a foreign replacement: %v", err)
	}
	if client.removeCalled || len(client.registerData) != 0 {
		t.Fatal("unsafe rollback reached a mutation")
	}
}

func TestRestoreDoesNotOverwriteForeignTaskAfterExistingSnapshot(t *testing.T) {
	scheduler := testScheduler(t)
	client := scheduler.Client.(*fakeClient)
	saved, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	foreign := managedState(t, scheduler, mustTaskName(t, scheduler, "production"), "production", false)
	foreign.XML = alterTaskCommand(t, foreign.XML, `C:\Other Product\other.exe`)
	client.state = foreign
	if err := scheduler.Restore(context.Background(), "production", saved); err == nil ||
		!strings.Contains(err.Error(), "unrelated task now occupies") {
		t.Fatalf("rollback overwrote a foreign replacement: %v", err)
	}
	if len(client.registerData) != 0 {
		t.Fatal("unsafe rollback reached task registration")
	}
}

func TestRestoreRejectsTamperedSnapshotIdentity(t *testing.T) {
	scheduler := testScheduler(t)
	client := scheduler.Client.(*fakeClient)
	data, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Current.XML = alterTaskCommand(t, saved.Current.XML, `C:\Forged\other.exe`)
	data, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Restore(context.Background(), "production", data); err == nil ||
		!strings.Contains(err.Error(), "unverified current task") {
		t.Fatalf("tampered snapshot was accepted: %v", err)
	}
	if client.removeCalled || len(client.registerData) != 0 {
		t.Fatal("tampered snapshot reached a mutation")
	}
}

func TestEnableMigratesOnlyOwnedLegacyTask(t *testing.T) {
	legacyNames, err := legacyTaskNames("production")
	if err != nil {
		t.Fatal(err)
	}
	for _, legacyName := range legacyNames {
		t.Run(legacyName, func(t *testing.T) {
			scheduler := testScheduler(t)
			client := &mappedClient{states: make(map[string]taskState)}
			client.states[legacyName] = managedState(t, scheduler, legacyName, "production", true)
			scheduler.Client = client
			profile := windowsProfile(config.WindowsSchedule{
				Frequency: config.WindowsFrequencyDaily,
				StartTime: "06:00",
			})
			if _, err := scheduler.Enable(context.Background(), "production", profile); err != nil {
				t.Fatal(err)
			}
			currentName := mustTaskName(t, scheduler, "production")
			if !client.states[currentName].Exists {
				t.Fatal("installation-scoped task was not registered")
			}
			if client.states[legacyName].Exists {
				t.Fatal("owned legacy task was not removed after migration")
			}
		})
	}
}

func TestMigrationDoesNotDeleteLegacyTaskFromAnotherInstallation(t *testing.T) {
	first := testScheduler(t)
	second := *first
	second.Paths.Config = filepath.Join(filepath.Dir(first.Paths.Config), "other-home", "config.toml")
	second.Paths.Credentials = filepath.Join(filepath.Dir(second.Paths.Config), "credentials.toml")
	second.Paths.StateDir = filepath.Join(filepath.Dir(second.Paths.Config), "state")
	legacyNames, err := legacyTaskNames("production")
	if err != nil {
		t.Fatal(err)
	}
	legacyName := legacyNames[0]
	foreign := managedState(t, &second, legacyName, "production", true)
	client := &mappedClient{states: map[string]taskState{legacyName: foreign}}
	first.Client = client
	second.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := first.Enable(context.Background(), "production", profile); err != nil {
		t.Fatal(err)
	}
	if !client.states[legacyName].Exists {
		t.Fatal("migration deleted a legacy task belonging to another installation")
	}
}

func TestStatusDistinguishesMissingFromUnavailable(t *testing.T) {
	scheduler := testScheduler(t)
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	scheduler.Client = &fakeClient{}
	missing := scheduler.Status(context.Background(), "production", profile)
	if !missing.Stale || missing.QueryError != nil || !strings.Contains(missing.UnitError, "does not exist") {
		t.Fatalf("legitimate missing task was not reported safely: %#v", missing)
	}
	scheduler.Client = &fakeClient{getErr: errors.New("RPC server unavailable")}
	unavailable := scheduler.Status(context.Background(), "production", profile)
	if !unavailable.Stale || unavailable.QueryError == nil ||
		!strings.Contains(unavailable.UnitError, "cannot determine") {
		t.Fatalf("unavailable Task Scheduler was not distinguished: %#v", unavailable)
	}
	scheduler.Client = &fakeClient{state: taskState{
		Exists:  true,
		Enabled: true,
		State:   99,
		XML: managedState(
			t,
			scheduler,
			mustTaskName(t, scheduler, "production"),
			"production",
			false,
		).XML,
	}}
	unexpected := scheduler.Status(context.Background(), "production", profile)
	if !unexpected.Stale || !strings.Contains(unexpected.UnitError, "inconsistent native state") {
		t.Fatalf("unexpected Task Scheduler state was accepted: %#v", unexpected)
	}
}

func TestRemoveAndRestoreAreVerified(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{state: taskState{Next: "2026-10-22T06:00:00+02:00"}}
	scheduler.Client = client
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := scheduler.Enable(context.Background(), "production", profile); err != nil {
		t.Fatal(err)
	}
	saved, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Remove(context.Background(), "production"); err != nil {
		t.Fatal(err)
	}
	if client.state.Exists {
		t.Fatal("task still exists after remove")
	}
	if err := scheduler.Restore(context.Background(), "production", saved); err != nil {
		t.Fatal(err)
	}
	if !client.state.Exists || !client.state.Enabled {
		t.Fatal("task snapshot was not restored")
	}
}

func TestMalformedOrForeignSnapshotsAreRejected(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{}
	scheduler.Client = client
	if err := scheduler.Restore(context.Background(), "production", []byte(`{"name":`)); err == nil {
		t.Fatal("malformed snapshot was accepted")
	}
	foreign := []byte(`{"version":2,"profile":"staging","current":{"name":"different","exists":false}}`)
	if err := scheduler.Restore(context.Background(), "production", foreign); err == nil {
		t.Fatal("snapshot from another profile was accepted")
	}
	if client.setEnabledCall || client.runCalled {
		t.Fatal("invalid snapshot caused a scheduler mutation")
	}
}

func TestRestoreHonorsCancellationBeforeMutation(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{}
	client.state = managedState(t, scheduler, mustTaskName(t, scheduler, "production"), "production", false)
	client.state.Next = "2026-10-22T06:00:00+02:00"
	scheduler.Client = client
	snapshot, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Restore(ctx, "production", snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %v", err)
	}
}

func TestRestorePasswordTaskRequiresSecretWithoutPersistingIt(t *testing.T) {
	scheduler := testScheduler(t)
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
		LogonMode: config.WindowsLogonPassword,
	})
	data, err := scheduler.GenerateXML("production", profile)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{state: taskState{
		Exists:  true,
		Enabled: true,
		State:   taskStateReady,
		Next:    "2026-10-22T06:00:00+02:00",
		XML:     data,
	}}
	scheduler.Client = client
	saved, err := scheduler.Snapshot(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	client.state = taskState{}
	if err := scheduler.Restore(context.Background(), "production", saved); err == nil ||
		!strings.Contains(err.Error(), "Windows password is required") {
		t.Fatalf("password rollback succeeded without a secret: %v", err)
	}
	const windowsPassword = "restore-only-windows-password"
	if err := scheduler.Restore(context.Background(), "production", saved, schedule.LogonSecret{
		WindowsPassword: windowsPassword,
	}); err != nil {
		t.Fatal(err)
	}
	if client.registration.LogonType != taskLogonPassword ||
		client.registration.UserID != scheduler.CurrentUser ||
		client.registration.Password != windowsPassword {
		t.Fatalf("unexpected restored registration: %#v", client.registration)
	}
	if strings.Contains(string(saved), windowsPassword) ||
		strings.Contains(string(client.state.XML), windowsPassword) {
		t.Fatal("Windows password was persisted in snapshot or XML")
	}
}

func TestEnableRequiresConfirmedNextRun(t *testing.T) {
	scheduler := testScheduler(t)
	scheduler.Client = &fakeClient{}
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := scheduler.Enable(context.Background(), "production", profile); err == nil ||
		!strings.Contains(err.Error(), "did not report a next execution") {
		t.Fatalf("unconfirmed activation was accepted: %v", err)
	}
}

func TestRunRequiresExistingTaskAndHonorsCancellation(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{}
	scheduler.Client = client
	if err := scheduler.Run(context.Background(), "production"); err == nil {
		t.Fatal("manual run succeeded for a missing task")
	}
	client.state = managedState(t, scheduler, mustTaskName(t, scheduler, "production"), "production", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Run(ctx, "production"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %v", err)
	}
	if client.runCalled {
		t.Fatal("manual run reached Task Scheduler after cancellation")
	}
}

func TestRunFailsWhenTaskSchedulerDoesNotStartTask(t *testing.T) {
	scheduler := testScheduler(t)
	scheduler.RunConfirmationTimeout = 150 * time.Millisecond
	client := scheduler.Client.(*fakeClient)
	client.state.LastResult = 0x41303
	err := scheduler.Run(context.Background(), "production")
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("accepted but unstarted task was reported as success: %v", err)
	}
	if !client.runCalled {
		t.Fatal("manual run request was not attempted")
	}
}

func TestTaskSchedulerTimeoutIsReported(t *testing.T) {
	scheduler := testScheduler(t)
	client := &fakeClient{}
	scheduler.Client = client
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	profile := windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	})
	if _, err := scheduler.Enable(ctx, "production", profile); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout was not propagated: %v", err)
	}
	if len(client.registerData) != 0 {
		t.Fatal("timed-out operation reached Task Scheduler registration")
	}
}

func TestLogsReadLatestProfileHistory(t *testing.T) {
	scheduler := testScheduler(t)
	store := history.New(scheduler.Paths.StateDir, filepath.Join(scheduler.Paths.StateDir, "logs"))
	for _, record := range []history.Record{
		{ID: "older", Profile: "production", StartedAt: time.Unix(1, 0)},
		{ID: "other", Profile: "staging", StartedAt: time.Unix(2, 0)},
		{ID: "latest", Profile: "production", StartedAt: time.Unix(3, 0)},
	} {
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
		log, err := store.StartLog(record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.WriteString(record.ID + "\nline two\n"); err != nil {
			t.Fatal(err)
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := scheduler.Logs(context.Background(), "production", 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "line two\n" {
		t.Fatalf("unexpected log output %q", data)
	}
}

func TestNextStartBoundaryPreservesLocalClockAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{
			name: "before spring transition",
			now:  time.Date(2026, 3, 7, 20, 0, 0, 0, location),
			want: "2026-03-08T06:00:00",
		},
		{
			name: "before autumn transition",
			now:  time.Date(2026, 10, 31, 20, 0, 0, 0, location),
			want: "2026-11-01T06:00:00",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := nextStartBoundary("06:00", testCase.now)
			if err != nil {
				t.Fatal(err)
			}
			if value := got.Format("2006-01-02T15:04:05"); value != testCase.want {
				t.Fatalf("boundary = %q, want %q", value, testCase.want)
			}
		})
	}
}

func TestTaskNextRunUsesWindowsLocalWallClock(t *testing.T) {
	location, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		date time.Time
		want string
	}{
		{
			name: "winter",
			date: time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC),
			want: "2026-01-15T06:00:00+01:00",
		},
		{
			name: "summer",
			date: time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC),
			want: "2026-07-15T06:00:00+02:00",
		},
		{
			name: "after spring DST transition",
			date: time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC),
			want: "2026-03-29T06:00:00+02:00",
		},
		{
			name: "after autumn DST transition",
			date: time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC),
			want: "2026-10-25T06:00:00+01:00",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := taskTimeInLocation(testCase.date, location).Format(time.RFC3339); got != testCase.want {
				t.Fatalf("local task time = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNeverRunTaskHasNoCosmeticLastRun(t *testing.T) {
	location, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	never := time.Date(1999, 11, 30, 0, 0, 0, 0, time.UTC)
	if value := formatTaskTime(never, location); value != "" {
		t.Fatalf("never-run task produced last_run=%q", value)
	}
}

func testScheduler(t *testing.T) *Scheduler {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "fivemesh-backup")
	if err := os.WriteFile(binary, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	scheduler := &Scheduler{
		BinaryPath:     binary,
		CurrentUser:    `DOMAIN\backup-user`,
		CurrentUserSID: "S-1-5-21-1000",
		Paths: config.Paths{
			Config:      filepath.Join(root, "config.toml"),
			Credentials: filepath.Join(root, "credentials.toml"),
			StateDir:    filepath.Join(root, "state"),
		},
		Now: func() time.Time {
			return time.Date(2026, 10, 21, 1, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
		},
		Client: &fakeClient{},
	}
	name, err := scheduler.taskName("production")
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Client.(*fakeClient).state = managedState(t, scheduler, name, "production", false)
	scheduler.Client.(*fakeClient).state.Next = "2026-10-22T06:00:00+02:00"
	return scheduler
}

func managedState(t *testing.T, scheduler *Scheduler, name, profileName string, legacy bool) taskState {
	t.Helper()
	data, err := scheduler.GenerateXML(profileName, windowsProfile(config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: "06:00",
	}))
	if err != nil {
		t.Fatal(err)
	}
	currentName, err := scheduler.taskName(profileName)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	text = strings.Replace(text,
		"<Description>"+scheduler.taskDescription(profileName, false)+"</Description>",
		"<Description>"+scheduler.taskDescription(profileName, legacy)+"</Description>",
		1,
	)
	text = strings.Replace(text, `<URI>\`+currentName+`</URI>`, `<URI>\`+name+`</URI>`, 1)
	return taskState{
		Exists:  true,
		Enabled: true,
		State:   taskStateReady,
		XML:     []byte(text),
	}
}

func mustTaskName(t *testing.T, scheduler *Scheduler, profileName string) string {
	t.Helper()
	name, err := scheduler.taskName(profileName)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func alterTaskCommand(t *testing.T, data []byte, command string) []byte {
	t.Helper()
	text := string(data)
	start := strings.Index(text, "<Command>")
	end := strings.Index(text, "</Command>")
	if start < 0 || end < start {
		t.Fatal("generated task XML has no command")
	}
	text = text[:start] + "<Command>" + command + "</Command>" + text[end+len("</Command>"):]
	return []byte(text)
}

func windowsProfile(schedule config.WindowsSchedule) config.Profile {
	return config.Profile{
		Schedule: config.Schedule{
			Mode:    config.ScheduleModeWindows,
			Windows: schedule,
		},
	}
}
