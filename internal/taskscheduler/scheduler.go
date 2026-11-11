package taskscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

const (
	taskStateDisabled = 1
	taskStateQueued   = 2
	taskStateReady    = 3
	taskStateRunning  = 4

	taskLogonPassword         = 1
	taskLogonS4U              = 2
	taskLogonInteractiveToken = 3
)

type taskState struct {
	Exists     bool
	Enabled    bool
	State      int32
	Next       string
	LastRun    string
	LastResult int32
	XML        []byte
}

type client interface {
	Validate(context.Context, []byte) error
	Get(context.Context, string) (taskState, error)
	Register(context.Context, string, []byte, taskRegistration) error
	SetEnabled(context.Context, string, bool) error
	Remove(context.Context, string) error
	Run(context.Context, string) error
}

type taskRegistration struct {
	LogonType int32
	UserID    string
	Password  string
	// SecurityDescriptor is an optional SDDL string applied to the task.
	// Background (S4U) tasks grant the owning account full control so that
	// later status, run, disable, and remove calls do not need elevation.
	SecurityDescriptor string
}

type Scheduler struct {
	BinaryPath             string
	Paths                  config.Paths
	CurrentUser            string
	CurrentUserSID         string
	Now                    func() time.Time
	RunConfirmationTimeout time.Duration
	Client                 client
}

var _ schedule.Manager = (*Scheduler)(nil)

func firstLogonSecret(secrets []schedule.LogonSecret) schedule.LogonSecret {
	if len(secrets) == 0 {
		return schedule.LogonSecret{}
	}
	return secrets[0]
}

type taskSnapshot struct {
	Name    string `json:"name"`
	Exists  bool   `json:"exists"`
	Enabled bool   `json:"enabled"`
	XML     []byte `json:"xml,omitempty"`
}

type snapshot struct {
	Version int            `json:"version"`
	Profile string         `json:"profile"`
	Current taskSnapshot   `json:"current"`
	Legacy  []taskSnapshot `json:"legacy,omitempty"`
}

func (s *Scheduler) Mode() string {
	return config.ScheduleModeWindows
}

func (s *Scheduler) Manages(mode string) bool {
	return mode == config.ScheduleModeWindows
}

func (s *Scheduler) Validate(ctx context.Context, profileName string, profile config.Profile) error {
	if err := s.validateProfile(profileName, profile); err != nil {
		return err
	}
	data, err := s.GenerateXML(profileName, profile)
	if err != nil {
		return err
	}
	if s.Client == nil {
		return errors.New("Windows Task Scheduler client is unavailable")
	}
	if err := s.Client.Validate(ctx, data); err != nil {
		return fmt.Errorf("validate Task Scheduler definition: %w", err)
	}
	return nil
}

func (s *Scheduler) queryTask(
	ctx context.Context,
	name string,
	profileName string,
	legacy bool,
) (taskState, bool, error) {
	state, err := s.Client.Get(ctx, name)
	if err != nil {
		return taskState{}, false, fmt.Errorf("query Windows task %s: %w", name, err)
	}
	if !state.Exists {
		return state, false, nil
	}
	return state, s.isManagedTask(state.XML, name, profileName, legacy), nil
}

func (s *Scheduler) Enable(
	ctx context.Context,
	profileName string,
	profile config.Profile,
	secrets ...schedule.LogonSecret,
) (string, error) {
	if err := s.validateProfile(profileName, profile); err != nil {
		return "", err
	}
	name, err := s.taskName(profileName)
	if err != nil {
		return "", err
	}
	data, err := s.GenerateXML(profileName, profile)
	if err != nil {
		return "", err
	}
	if s.Client == nil {
		return "", errors.New("Windows Task Scheduler client is unavailable")
	}
	current, managed, err := s.queryTask(ctx, name, profileName, false)
	if err != nil {
		return "", fmt.Errorf("query Windows task %s before registration: %w", name, err)
	}
	if current.Exists && !managed {
		return "", fmt.Errorf("refusing to overwrite unrelated Windows task %s", name)
	}
	registration, err := s.registrationForTask(data, firstLogonSecret(secrets))
	if err != nil {
		return "", err
	}
	if err := s.Client.Register(ctx, name, data, registration); err != nil {
		return "", fmt.Errorf("register Windows task %s: %w", name, err)
	}
	state, err := s.Client.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("verify registered Windows task %s: %w", name, err)
	}
	if !state.Exists {
		return "", fmt.Errorf("Task Scheduler did not confirm task %s exists", name)
	}
	if !s.isManagedTask(state.XML, name, profileName, false) {
		return "", fmt.Errorf("Task Scheduler registered task %s with an unexpected identity", name)
	}
	if !state.Enabled {
		return "", fmt.Errorf("Task Scheduler did not confirm task %s as enabled", name)
	}
	if state.Next == "" {
		return "", fmt.Errorf("Task Scheduler enabled task %s but did not report a next execution time", name)
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return "", err
	}
	for _, legacyName := range legacyNames {
		legacyState, managed, queryErr := s.queryTask(ctx, legacyName, profileName, true)
		if queryErr != nil {
			return "", fmt.Errorf("query legacy Windows task %s: %w", legacyName, queryErr)
		}
		if !legacyState.Exists || !managed {
			continue
		}
		if removeErr := s.Client.Remove(ctx, legacyName); removeErr != nil {
			return "", fmt.Errorf("remove migrated legacy Windows task %s: %w", legacyName, removeErr)
		}
		after, verifyErr := s.Client.Get(ctx, legacyName)
		if verifyErr != nil {
			return "", fmt.Errorf("verify migrated legacy task %s removal: %w", legacyName, verifyErr)
		}
		if after.Exists {
			return "", fmt.Errorf("Task Scheduler did not confirm removal of legacy Windows task %s", legacyName)
		}
	}
	return state.Next, nil
}

func (s *Scheduler) Disable(ctx context.Context, profileName string) error {
	name, err := s.taskName(profileName)
	if err != nil {
		return err
	}
	if s.Client == nil {
		return errors.New("Windows Task Scheduler client is unavailable")
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return err
	}
	targets := append([]string{name}, legacyNames...)
	for index, target := range targets {
		legacy := index > 0
		state, managed, queryErr := s.queryTask(ctx, target, profileName, legacy)
		if queryErr != nil {
			return fmt.Errorf("query Windows task %s before disable: %w", target, queryErr)
		}
		if !state.Exists {
			continue
		}
		if !managed {
			if legacy {
				continue
			}
			return fmt.Errorf("refusing to disable unrelated Windows task %s", target)
		}
		if state.Enabled {
			if setErr := s.Client.SetEnabled(ctx, target, false); setErr != nil {
				return fmt.Errorf("disable Windows task %s: %w", target, setErr)
			}
		}
		after, afterErr := s.Client.Get(ctx, target)
		if afterErr != nil {
			return fmt.Errorf("verify disabled Windows task %s: %w", target, afterErr)
		}
		if !after.Exists || after.Enabled || !s.isManagedTask(after.XML, target, profileName, legacy) {
			return fmt.Errorf("Task Scheduler did not confirm Windows task %s as disabled", target)
		}
	}
	return nil
}

func (s *Scheduler) Remove(ctx context.Context, profileName string) error {
	name, err := s.taskName(profileName)
	if err != nil {
		return err
	}
	if s.Client == nil {
		return errors.New("Windows Task Scheduler client is unavailable")
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return err
	}
	targets := append([]string{name}, legacyNames...)
	for index, target := range targets {
		legacy := index > 0
		state, managed, queryErr := s.queryTask(ctx, target, profileName, legacy)
		if queryErr != nil {
			return fmt.Errorf("query Windows task %s before remove: %w", target, queryErr)
		}
		if !state.Exists {
			continue
		}
		if !managed {
			if legacy {
				continue
			}
			return fmt.Errorf("refusing to remove unrelated Windows task %s", target)
		}
		if removeErr := s.Client.Remove(ctx, target); removeErr != nil {
			return fmt.Errorf("remove Windows task %s: %w", target, removeErr)
		}
		after, afterErr := s.Client.Get(ctx, target)
		if afterErr != nil {
			return fmt.Errorf("verify removed Windows task %s: %w", target, afterErr)
		}
		if after.Exists {
			return fmt.Errorf("Task Scheduler did not confirm removal of Windows task %s", target)
		}
	}
	return nil
}

func (s *Scheduler) Snapshot(ctx context.Context, profileName string) ([]byte, error) {
	name, err := s.taskName(profileName)
	if err != nil {
		return nil, err
	}
	if s.Client == nil {
		return nil, errors.New("Windows Task Scheduler client is unavailable")
	}
	state, managed, err := s.queryTask(ctx, name, profileName, false)
	if err != nil {
		return nil, fmt.Errorf("cannot determine Task Scheduler state for snapshot: %w", err)
	}
	if state.Exists && !managed {
		return nil, fmt.Errorf("refusing to snapshot unrelated Windows task %s", name)
	}
	saved := snapshot{
		Version: 2,
		Profile: profileName,
		Current: taskSnapshot{
			Name:    name,
			Exists:  state.Exists,
			Enabled: state.Enabled,
			XML:     state.XML,
		},
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return nil, err
	}
	for _, legacyName := range legacyNames {
		legacyState, legacyManaged, queryErr := s.queryTask(ctx, legacyName, profileName, true)
		if queryErr != nil {
			return nil, fmt.Errorf("cannot determine legacy Task Scheduler state for snapshot: %w", queryErr)
		}
		if legacyState.Exists && legacyManaged {
			saved.Legacy = append(saved.Legacy, taskSnapshot{
				Name:    legacyName,
				Exists:  true,
				Enabled: legacyState.Enabled,
				XML:     legacyState.XML,
			})
		}
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return nil, fmt.Errorf("encode Task Scheduler snapshot: %w", err)
	}
	return data, nil
}

func (s *Scheduler) SnapshotRequiresPassword(data []byte) bool {
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return false
	}
	if saved.Current.Exists && taskLogonMode(saved.Current.XML) == "password" {
		return true
	}
	for _, task := range saved.Legacy {
		if task.Exists && taskLogonMode(task.XML) == "password" {
			return true
		}
	}
	return false
}

func (s *Scheduler) Restore(
	ctx context.Context,
	profileName string,
	data []byte,
	secrets ...schedule.LogonSecret,
) error {
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("decode Task Scheduler snapshot: %w", err)
	}
	if saved.Version != 2 || saved.Profile != profileName {
		return errors.New("Task Scheduler snapshot is malformed or belongs to a different profile")
	}
	name, err := s.taskName(profileName)
	if err != nil {
		return err
	}
	if saved.Current.Name != name {
		return errors.New("Task Scheduler snapshot current task belongs to a different installation")
	}
	if saved.Current.Exists &&
		!s.isManagedTask(saved.Current.XML, name, profileName, false) {
		return errors.New("Task Scheduler snapshot contains an unverified current task")
	}
	if s.Client == nil {
		return errors.New("Windows Task Scheduler client is unavailable")
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return err
	}
	allowedLegacy := make(map[string]struct{}, len(legacyNames))
	for _, legacyName := range legacyNames {
		allowedLegacy[legacyName] = struct{}{}
	}
	for _, task := range saved.Legacy {
		if _, ok := allowedLegacy[task.Name]; !ok {
			return fmt.Errorf("Task Scheduler snapshot contains unexpected legacy task %s", task.Name)
		}
		if !task.Exists || !s.isManagedTask(task.XML, task.Name, profileName, true) {
			return fmt.Errorf("Task Scheduler snapshot contains an unverified legacy task %s", task.Name)
		}
	}

	// Preflight every live occupant before changing anything. A foreign task
	// that appeared after the snapshot makes the old state impossible to restore
	// safely.
	targets := []struct {
		snapshot taskSnapshot
		legacy   bool
	}{
		{snapshot: saved.Current},
	}
	for _, task := range saved.Legacy {
		targets = append(targets, struct {
			snapshot taskSnapshot
			legacy   bool
		}{snapshot: task, legacy: true})
	}
	for _, target := range targets {
		live, managed, queryErr := s.queryTask(ctx, target.snapshot.Name, profileName, target.legacy)
		if queryErr != nil {
			return fmt.Errorf("verify live occupant of Windows task %s before rollback: %w", target.snapshot.Name, queryErr)
		}
		if live.Exists && !managed {
			return fmt.Errorf(
				"cannot safely restore Windows task %s: an unrelated task now occupies that name",
				target.snapshot.Name,
			)
		}
	}

	for _, target := range targets {
		live, managed, queryErr := s.queryTask(ctx, target.snapshot.Name, profileName, target.legacy)
		if queryErr != nil {
			return fmt.Errorf("recheck Windows task %s before rollback mutation: %w", target.snapshot.Name, queryErr)
		}
		if live.Exists && !managed {
			return fmt.Errorf(
				"cannot safely restore Windows task %s: an unrelated task appeared during rollback",
				target.snapshot.Name,
			)
		}
		if target.snapshot.Exists {
			if len(target.snapshot.XML) == 0 {
				return fmt.Errorf("Task Scheduler snapshot contains no XML for %s", target.snapshot.Name)
			}
			registration, credentialErr := s.registrationForTask(target.snapshot.XML, firstLogonSecret(secrets))
			if credentialErr != nil {
				return fmt.Errorf("prepare Windows task %s registration: %w", target.snapshot.Name, credentialErr)
			}
			if registerErr := s.Client.Register(ctx, target.snapshot.Name, target.snapshot.XML, registration); registerErr != nil {
				return fmt.Errorf("restore Windows task %s registration: %w", target.snapshot.Name, registerErr)
			}
			if setErr := s.Client.SetEnabled(ctx, target.snapshot.Name, target.snapshot.Enabled); setErr != nil {
				return fmt.Errorf("restore Windows task %s enabled state: %w", target.snapshot.Name, setErr)
			}
		} else if live.Exists {
			if removeErr := s.Client.Remove(ctx, target.snapshot.Name); removeErr != nil {
				return fmt.Errorf("remove Windows task %s absent from snapshot: %w", target.snapshot.Name, removeErr)
			}
		}
		after, afterErr := s.Client.Get(ctx, target.snapshot.Name)
		if afterErr != nil {
			return fmt.Errorf("verify restored Windows task %s: %w", target.snapshot.Name, afterErr)
		}
		if after.Exists != target.snapshot.Exists {
			return fmt.Errorf("restored Windows task %s existence does not match the snapshot", target.snapshot.Name)
		}
		if after.Exists &&
			(after.Enabled != target.snapshot.Enabled ||
				!s.isManagedTask(after.XML, target.snapshot.Name, profileName, target.legacy)) {
			return fmt.Errorf("restored Windows task %s state does not match the snapshot", target.snapshot.Name)
		}
	}
	return nil
}

func (s *Scheduler) Status(ctx context.Context, profileName string, profile config.Profile) schedule.Status {
	mode := profile.Schedule.Mode
	if mode == "" {
		mode = config.ScheduleModeManual
	}
	status := schedule.Status{
		Profile:      profileName,
		Backend:      s.Mode(),
		Mode:         mode,
		Enabled:      "unknown",
		Active:       "unknown",
		Resource:     "unavailable",
		ResourceKind: "task",
		LogonMode:    "unknown",
	}
	name, err := s.taskName(profileName)
	if err != nil {
		status.UnitError = err.Error()
		return status
	}
	status.Resource = name
	if s.Client == nil {
		status.Stale = true
		status.UnitError = "Windows Task Scheduler client is unavailable; task state is unknown"
		return status
	}
	state, managed, queryErr := s.queryTask(ctx, name, profileName, false)
	if queryErr != nil {
		status.Stale = true
		status.QueryError = queryErr
		status.UnitError = "cannot determine Windows Task Scheduler state: " + queryErr.Error()
		return status
	}
	if state.Exists && !managed {
		status.Stale = true
		status.UnitError = fmt.Sprintf("Windows task %s exists but is not owned by this FiveMesh profile", name)
		return status
	}
	legacyNames, legacyErr := legacyTaskNames(profileName)
	if legacyErr != nil {
		status.Stale = true
		status.UnitError = legacyErr.Error()
		return status
	}
	var managedLegacy []struct {
		name  string
		state taskState
	}
	for _, legacyName := range legacyNames {
		legacyState, legacyManaged, legacyQueryErr := s.queryTask(ctx, legacyName, profileName, true)
		if legacyQueryErr != nil {
			status.Stale = true
			status.QueryError = legacyQueryErr
			status.UnitError = "cannot determine legacy Windows Task Scheduler state: " + legacyQueryErr.Error()
			return status
		}
		if legacyState.Exists && legacyManaged {
			managedLegacy = append(managedLegacy, struct {
				name  string
				state taskState
			}{name: legacyName, state: legacyState})
		}
	}
	legacyOnly := false
	if !state.Exists && len(managedLegacy) > 0 {
		state = managedLegacy[0].state
		legacyOnly = true
		status.Resource = managedLegacy[0].name
	}
	if !state.Exists {
		status.Enabled = "false"
		status.Active = "not-found"
	} else {
		if !validTaskState(state.State) || (state.Enabled && state.State == taskStateDisabled) {
			status.Stale = true
			status.UnitError = fmt.Sprintf("Windows task %s reported inconsistent native state", name)
			return status
		}
		status.Enabled = fmt.Sprintf("%t", state.Enabled)
		status.Active = taskStateName(state.State)
		status.Next = state.Next
		status.LastRun = state.LastRun
		status.LastResult = fmt.Sprintf("0x%08X", uint32(state.LastResult))
		status.LogonMode = displayLogonMode(taskLogonMode(state.XML))
	}
	if profile.Schedule.Mode != config.ScheduleModeWindows {
		if state.Exists || len(managedLegacy) > 0 {
			status.Stale = true
			status.UnitError = "profile is configured for manual backups but a Windows task exists; run \"fivemesh-backup schedule remove --profile " + profileName + "\""
		}
		return status
	}
	if !state.Exists {
		status.Stale = true
		status.UnitError = "Windows scheduling is enabled in configuration but the task does not exist; run \"fivemesh-backup schedule enable --profile " + profileName + "\""
		return status
	}
	if legacyOnly {
		status.Stale = true
		status.UnitError = "a legacy Windows task is active but has not been migrated; run \"fivemesh-backup schedule update --profile " + profileName + "\""
		return status
	}
	if len(managedLegacy) > 0 {
		status.Stale = true
		status.UnitError = "a legacy Windows task remains alongside the installation-scoped task; run \"fivemesh-backup schedule update --profile " + profileName + "\""
		return status
	}
	if actualLogonMode := taskLogonMode(state.XML); actualLogonMode != profile.Schedule.Windows.EffectiveLogonMode() {
		status.Stale = true
		status.UnitError = fmt.Sprintf(
			"Windows task logon mode is %s but configuration requires %s; run \"fivemesh-backup schedule update --profile %s\"",
			actualLogonMode,
			profile.Schedule.Windows.EffectiveLogonMode(),
			profileName,
		)
		return status
	}
	if profile.Schedule.Paused {
		if state.Enabled {
			status.Stale = true
			status.UnitError = "automatic backups are marked paused but the Windows task is still enabled"
		}
		return status
	}
	if !state.Enabled {
		status.Stale = true
		status.UnitError = "automatic backups are enabled in configuration but the Windows task is disabled"
	}
	return status
}

func displayLogonMode(mode string) string {
	switch mode {
	case "background":
		return "background (runs whether or not you are signed in; no password stored)"
	case "interactive":
		return "interactive (current user must be logged in)"
	case "password":
		return "password (Task Scheduler manages logon; no user session required)"
	default:
		return "unknown"
	}
}

func (s *Scheduler) Inspect(ctx context.Context, profileName string, profile config.Profile) (bool, string, error) {
	status := s.Status(ctx, profileName, profile)
	return status.Stale, status.UnitError, status.QueryError
}

func (s *Scheduler) effectiveTask(ctx context.Context, profileName string) (string, taskState, error) {
	name, err := s.taskName(profileName)
	if err != nil {
		return "", taskState{}, err
	}
	state, managed, err := s.queryTask(ctx, name, profileName, false)
	if err != nil {
		return "", taskState{}, err
	}
	if state.Exists {
		if !managed {
			return "", taskState{}, fmt.Errorf("refusing to use unrelated Windows task %s", name)
		}
		return name, state, nil
	}
	legacyNames, err := legacyTaskNames(profileName)
	if err != nil {
		return "", taskState{}, err
	}
	var foundName string
	var found taskState
	for _, legacyName := range legacyNames {
		legacyState, legacyManaged, queryErr := s.queryTask(ctx, legacyName, profileName, true)
		if queryErr != nil {
			return "", taskState{}, queryErr
		}
		if !legacyState.Exists || !legacyManaged {
			continue
		}
		if foundName != "" {
			return "", taskState{}, fmt.Errorf("multiple legacy Windows tasks are associated with profile %q", profileName)
		}
		foundName = legacyName
		found = legacyState
	}
	if foundName == "" {
		return "", taskState{}, fmt.Errorf("Windows task for profile %q does not exist", profileName)
	}
	return foundName, found, nil
}

func (s *Scheduler) Next(ctx context.Context, profileName string, _ config.Profile) (string, error) {
	if s.Client == nil {
		return "", errors.New("Windows Task Scheduler client is unavailable")
	}
	_, state, err := s.effectiveTask(ctx, profileName)
	if err != nil {
		return "", fmt.Errorf("query Windows task next execution: %w", err)
	}
	if !state.Enabled {
		return "", errors.New("Windows task is disabled")
	}
	if state.Next == "" {
		return "", errors.New("Windows task has no next execution time")
	}
	return state.Next, nil
}

func (s *Scheduler) Run(ctx context.Context, profileName string) error {
	if s.Client == nil {
		return errors.New("Windows Task Scheduler client is unavailable")
	}
	name, before, err := s.effectiveTask(ctx, profileName)
	if err != nil {
		return fmt.Errorf("query Windows task before run: %w", err)
	}
	if err := s.Client.Run(ctx, name); err != nil {
		return fmt.Errorf("start Windows task %s: %w", name, err)
	}
	timeout := s.RunConfirmationTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		after, queryErr := s.Client.Get(ctx, name)
		if queryErr != nil {
			return fmt.Errorf("verify manual run of Windows task %s: %w", name, queryErr)
		}
		if after.State == taskStateRunning ||
			(after.LastRun != "" && after.LastRun != before.LastRun) {
			return nil
		}
		if after.LastResult != before.LastResult && after.LastResult != 0x41303 {
			return fmt.Errorf(
				"Task Scheduler accepted the request but Windows task %s did not start (last result 0x%08X)",
				name,
				uint32(after.LastResult),
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf(
				"Task Scheduler accepted the request but Windows task %s did not start within %s (last result 0x%08X)",
				name,
				timeout,
				uint32(after.LastResult),
			)
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) Logs(_ context.Context, profileName string, lines int) ([]byte, error) {
	if lines <= 0 {
		lines = 50
	}
	store := history.New(s.Paths.StateDir, filepath.Join(s.Paths.StateDir, "logs"))
	records, _, err := store.Read()
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.Profile != profileName {
			continue
		}
		data, err := store.ReadLog(record.ID)
		if err != nil {
			return nil, err
		}
		linesData := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(linesData) > lines {
			linesData = linesData[len(linesData)-lines:]
		}
		result := strings.Join(linesData, "\n")
		if result != "" {
			result += "\n"
		}
		return []byte(result), nil
	}
	return nil, fmt.Errorf("no execution log found for profile %q", profileName)
}

func (s *Scheduler) validateProfile(profileName string, profile config.Profile) error {
	if profile.Schedule.Mode != config.ScheduleModeWindows {
		return errors.New("profile scheduling is not configured for Windows Task Scheduler")
	}
	if profile.Schedule.Windows.Frequency == "" {
		return errors.New("Windows schedule configuration is missing")
	}
	if _, err := s.taskName(profileName); err != nil {
		return err
	}
	if !filepath.IsAbs(s.BinaryPath) {
		return errors.New("scheduled executable path must be absolute")
	}
	if err := ensureRegularFile(s.BinaryPath, "scheduled"); err != nil {
		return err
	}
	for label, path := range map[string]string{
		"config":      s.Paths.Config,
		"credentials": s.Paths.Credentials,
		"state":       s.Paths.StateDir,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute for Task Scheduler", label)
		}
	}
	if err := profile.Schedule.Windows.Validate(); err != nil {
		return err
	}
	if profile.Schedule.Windows.EffectiveLogonMode() == config.WindowsLogonBackground &&
		isNetworkPath(profile.Backup.Directory) {
		return fmt.Errorf(
			"backup directory %q is a network location; background mode cannot use Windows network credentials. "+
				"Choose a local folder, or use --logon-mode password",
			profile.Backup.Directory,
		)
	}
	return nil
}

// isNetworkPath reports whether path is a UNC path or, on Windows, a mapped
// network drive. Background (S4U) tasks have no network credentials.
func isNetworkPath(path string) bool {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	return isRemoteDrive(path)
}

func taskStateName(state int32) string {
	switch state {
	case taskStateDisabled:
		return "disabled"
	case taskStateQueued:
		return "queued"
	case taskStateReady:
		return "ready"
	case taskStateRunning:
		return "running"
	default:
		return "unknown"
	}
}

func validTaskState(state int32) bool {
	return state >= taskStateDisabled && state <= taskStateRunning
}

func taskTimeInLocation(value time.Time, location *time.Location) time.Time {
	if location == nil {
		location = time.Local
	}
	// OLE automation dates contain local wall-clock fields without an offset.
	// go-ole currently labels those components as UTC, which must not be
	// converted with time.Time.Local.
	return time.Date(
		value.Year(),
		value.Month(),
		value.Day(),
		value.Hour(),
		value.Minute(),
		value.Second(),
		value.Nanosecond(),
		location,
	)
}

func formatTaskTime(value time.Time, location *time.Location) string {
	if value.IsZero() || value.Year() < 2000 {
		return ""
	}
	return taskTimeInLocation(value, location).Format(time.RFC3339)
}

func validPrincipal(name string) bool {
	if strings.TrimSpace(name) == "" || len(name) > 1024 {
		return false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().Local()
	}
	return time.Now().Local()
}

func ensureRegularFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s executable: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s executable is not a regular file", label)
	}
	return nil
}
