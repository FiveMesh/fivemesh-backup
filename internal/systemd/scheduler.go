package systemd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

type CommandFailure struct {
	Command  string
	Args     []string
	ExitCode int
	Output   string
	Err      error
}

func (e *CommandFailure) Error() string {
	detail := strings.TrimSpace(e.Output)
	if detail == "" {
		return fmt.Sprintf("%s failed: %v", e.Command, e.Err)
	}
	return fmt.Sprintf("%s failed: %v: %s", e.Command, e.Err, detail)
}

func (e *CommandFailure) Unwrap() error {
	return e.Err
}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(ctxErr, err)
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return output, &CommandFailure{
			Command:  name,
			Args:     append([]string(nil), args...),
			ExitCode: exitCode,
			Output:   string(output),
			Err:      err,
		}
	}
	return output, nil
}

type Scheduler struct {
	UnitDir     string
	BinaryPath  string
	Paths       config.Paths
	Command     CommandRunner
	CurrentUser string
}

var _ schedule.Manager = (*Scheduler)(nil)

type Status = schedule.Status

type timerState struct {
	LoadState     string
	ActiveState   string
	UnitFileState string
}

func (s timerState) Exists() bool {
	return s.LoadState != "not-found"
}

func (s timerState) Enabled() bool {
	return s.UnitFileState == "enabled" || s.UnitFileState == "enabled-runtime"
}

func (s timerState) Active() bool {
	return s.ActiveState == "active"
}

func (s timerState) SafelyInactive() bool {
	return !s.Enabled() && (s.ActiveState == "inactive" || s.ActiveState == "failed")
}

func (s timerState) String() string {
	return fmt.Sprintf("load=%s active=%s enabled=%s", s.LoadState, s.ActiveState, displayUnitFileState(s.UnitFileState))
}

type stateSnapshot struct {
	ServiceName   string `json:"service_name"`
	TimerName     string `json:"timer_name"`
	ServiceData   []byte `json:"service_data,omitempty"`
	TimerData     []byte `json:"timer_data,omitempty"`
	ServiceExists bool   `json:"service_exists"`
	TimerExists   bool   `json:"timer_exists"`
	Enabled       bool   `json:"enabled"`
	Active        bool   `json:"active"`
}

func New(paths config.Paths) (*Scheduler, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve current executable: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	return &Scheduler{
		UnitDir:     filepath.Join(configHome, "systemd", "user"),
		BinaryPath:  binary,
		Paths:       paths,
		Command:     ExecRunner{},
		CurrentUser: currentUser.Username,
	}, nil
}

func (s *Scheduler) Mode() string {
	return config.ScheduleModeSystemd
}

func (s *Scheduler) Manages(mode string) bool {
	return mode == config.ScheduleModeSystemd
}

func UnitNames(profile string) (service, timer string, err error) {
	if strings.TrimSpace(profile) == "" {
		return "", "", errors.New("profile name is required for systemd units")
	}
	for _, char := range profile {
		if char == '/' || char == '\\' || unicode.IsControl(char) {
			return "", "", fmt.Errorf("profile %q cannot be used for systemd units", profile)
		}
	}
	hash := sha256.Sum256([]byte(profile))
	suffix := hex.EncodeToString(hash[:4])
	var slug strings.Builder
	for _, char := range strings.ToLower(profile) {
		switch {
		case char >= 'a' && char <= 'z':
			slug.WriteRune(char)
		case char >= '0' && char <= '9':
			slug.WriteRune(char)
		case char == '-', char == '_':
			slug.WriteRune(char)
		default:
			slug.WriteByte('-')
		}
	}
	name := strings.Trim(slug.String(), "-_")
	if name == "" {
		name = "profile"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	base := "fivemesh-backup-" + name + "-" + suffix
	return base + ".service", base + ".timer", nil
}

func (s *Scheduler) Enable(
	ctx context.Context,
	profileName string,
	profile config.Profile,
	_ ...schedule.LogonSecret,
) (string, error) {
	if profile.Schedule.Mode != "systemd" {
		return "", errors.New("profile scheduling is not configured for systemd")
	}
	if err := s.Validate(ctx, profileName, profile); err != nil {
		return "", err
	}
	service, timer, err := UnitNames(profileName)
	if err != nil {
		return "", err
	}
	serviceData, timerData, err := s.Generate(profileName, profile)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.UnitDir, 0o755); err != nil {
		return "", fmt.Errorf("create systemd user unit directory: %w", err)
	}
	if err := writeAtomic(filepath.Join(s.UnitDir, service), serviceData, 0o644); err != nil {
		return "", fmt.Errorf("write service unit: %w", err)
	}
	if err := writeAtomic(filepath.Join(s.UnitDir, timer), timerData, 0o644); err != nil {
		return "", fmt.Errorf("write timer unit: %w", err)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return "", fmt.Errorf("reload systemd user units: %w", err)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "enable", "--now", timer); err != nil {
		return "", fmt.Errorf("enable systemd timer %s: %w", timer, err)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "restart", timer); err != nil {
		return "", fmt.Errorf("apply updated systemd timer %s: %w", timer, err)
	}
	if err := s.verifyActive(ctx, timer); err != nil {
		return "", err
	}
	next, err := s.Next(ctx, profileName, profile)
	if err != nil {
		return "", fmt.Errorf("timer activated but next execution could not be confirmed: %w", err)
	}
	return next, nil
}

func (s *Scheduler) EnsureLinger(ctx context.Context) error {
	if s.CurrentUser == "" {
		return errors.New("cannot determine current user for systemd user scheduling")
	}
	output, err := s.run(ctx, "loginctl", "show-user", s.CurrentUser, "--property=Linger", "--value")
	if err == nil && strings.EqualFold(strings.TrimSpace(string(output)), "yes") {
		return nil
	}
	if _, enableErr := s.run(ctx, "loginctl", "enable-linger", s.CurrentUser); enableErr != nil {
		return fmt.Errorf(
			"systemd user timers need lingering after logout; ask an administrator to run \"loginctl enable-linger %s\": %w",
			s.CurrentUser,
			enableErr,
		)
	}
	return nil
}

func (s *Scheduler) Disable(ctx context.Context, profileName string) error {
	_, timer, err := UnitNames(profileName)
	if err != nil {
		return err
	}
	return s.disableAndVerify(ctx, timer)
}

func (s *Scheduler) Remove(ctx context.Context, profileName string) error {
	service, timer, err := UnitNames(profileName)
	if err != nil {
		return err
	}
	if err := s.Disable(ctx, profileName); err != nil {
		return err
	}
	for _, path := range []string{
		filepath.Join(s.UnitDir, timer),
		filepath.Join(s.UnitDir, service),
	} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove systemd unit %q: %w", filepath.Base(path), err)
		}
	}
	if _, err := s.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd user units: %w", err)
	}
	return nil
}

func (s *Scheduler) Run(ctx context.Context, profileName string) error {
	service, _, err := UnitNames(profileName)
	if err != nil {
		return err
	}
	if _, err := s.run(ctx, "systemctl", "--user", "start", service); err != nil {
		return fmt.Errorf("start scheduled backup service %s: %w", service, err)
	}
	return nil
}

func (s *Scheduler) Status(ctx context.Context, profileName string, profile config.Profile) Status {
	service, timer, err := UnitNames(profileName)
	mode := profile.Schedule.Mode
	if mode == "" {
		mode = "manual"
	}
	status := Status{
		Profile:      profileName,
		Backend:      s.Mode(),
		Mode:         mode,
		Resource:     timer,
		ResourceKind: "timer",
		Service:      service,
		Timer:        timer,
		Enabled:      "unknown",
		Active:       "unknown",
	}
	if err != nil {
		status.UnitError = err.Error()
		return status
	}
	state, queryErr := s.queryTimerState(ctx, timer)
	if queryErr != nil {
		status.Stale = true
		status.QueryError = queryErr
		status.UnitError = "cannot determine systemd timer state: " + queryErr.Error()
		return status
	}
	status.Enabled = displayUnitFileState(state.UnitFileState)
	status.Active = state.ActiveState
	_, serviceErr := os.Stat(filepath.Join(s.UnitDir, service))
	_, timerErr := os.Stat(filepath.Join(s.UnitDir, timer))
	unitsExist := serviceErr == nil && timerErr == nil
	if mode != "systemd" {
		if unitsExist || state.Enabled() || state.Active() || state.Exists() {
			status.Stale = true
			status.UnitError = "profile is configured for manual backups but systemd timer state exists; run \"fivemesh-backup schedule remove --profile " + profileName + "\""
		}
		return status
	}
	if !unitsExist {
		status.Stale = true
		status.UnitError = "systemd configuration is enabled but one or more unit files are missing; run \"fivemesh-backup schedule enable --profile " + profileName + "\""
	}
	timerEnabled := state.Enabled()
	timerActive := state.Active()
	if profile.Schedule.Paused {
		if timerEnabled || timerActive {
			status.Stale = true
			status.UnitError = "automatic backups are marked paused but the timer is still enabled or active"
		}
		return status
	}
	if !timerEnabled || !timerActive {
		status.Stale = true
		status.UnitError = "automatic backups are enabled in configuration but the timer is not enabled and active"
	}
	if timerEnabled && timerActive {
		next, nextErr := s.Next(ctx, profileName, profile)
		if nextErr != nil {
			status.Stale = true
			status.UnitError = nextErr.Error()
		} else {
			status.Next = next
		}
	}
	return status
}

func (s *Scheduler) Inspect(ctx context.Context, profileName string, profile config.Profile) (bool, string, error) {
	status := s.Status(ctx, profileName, profile)
	return status.Stale, status.UnitError, status.QueryError
}

func (s *Scheduler) Validate(ctx context.Context, profileName string, profile config.Profile) error {
	if profile.Schedule.Mode != "systemd" {
		return errors.New("profile scheduling is not configured for systemd")
	}
	calendar := strings.TrimSpace(profile.Schedule.OnCalendar)
	if profile.Schedule.Timezone != "" {
		calendar += " " + profile.Schedule.Timezone
	}
	if err := s.ValidateCalendar(ctx, calendar); err != nil {
		return err
	}
	service, timer, err := s.Generate(profileName, profile)
	if err != nil {
		return err
	}
	if err := s.verifyUnitFiles(ctx, service, timer); err != nil {
		return err
	}
	if err := s.verifyUnitDirectory(); err != nil {
		return err
	}
	return s.EnsureLinger(ctx)
}

func (s *Scheduler) Snapshot(ctx context.Context, profileName string) ([]byte, error) {
	service, timer, err := UnitNames(profileName)
	if err != nil {
		return nil, err
	}
	snapshot := stateSnapshot{ServiceName: service, TimerName: timer}
	if data, readErr := os.ReadFile(filepath.Join(s.UnitDir, service)); readErr == nil {
		snapshot.ServiceExists = true
		snapshot.ServiceData = data
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read existing service unit: %w", readErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(s.UnitDir, timer)); readErr == nil {
		snapshot.TimerExists = true
		snapshot.TimerData = data
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read existing timer unit: %w", readErr)
	}
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return nil, fmt.Errorf("cannot determine current timer state for snapshot: %w", err)
	}
	snapshot.Enabled = state.Enabled()
	snapshot.Active = state.Active()
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode scheduler snapshot: %w", err)
	}
	return data, nil
}

func (s *Scheduler) SnapshotRequiresPassword([]byte) bool {
	return false
}

func (s *Scheduler) Restore(
	ctx context.Context,
	profileName string,
	data []byte,
	_ ...schedule.LogonSecret,
) error {
	var snapshot stateSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode scheduler snapshot: %w", err)
	}
	service, timer, err := UnitNames(profileName)
	if err != nil {
		return err
	}
	if snapshot.ServiceName != service || snapshot.TimerName != timer {
		return errors.New("scheduler snapshot belongs to a different profile")
	}
	if err := os.MkdirAll(s.UnitDir, 0o755); err != nil {
		return fmt.Errorf("create systemd user unit directory: %w", err)
	}
	if err := restoreUnit(filepath.Join(s.UnitDir, service), snapshot.ServiceData, snapshot.ServiceExists); err != nil {
		return fmt.Errorf("restore service unit: %w", err)
	}
	if err := restoreUnit(filepath.Join(s.UnitDir, timer), snapshot.TimerData, snapshot.TimerExists); err != nil {
		return fmt.Errorf("restore timer unit: %w", err)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload restored systemd units: %w", err)
	}
	if !snapshot.TimerExists {
		return s.disableAndVerify(ctx, timer)
	}
	current, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return fmt.Errorf("query timer state before restore: %w", err)
	}
	if snapshot.Enabled && !current.Enabled() {
		if _, err := s.run(ctx, "systemctl", "--user", "enable", timer); err != nil {
			return fmt.Errorf("re-enable restored timer: %w", err)
		}
	} else if !snapshot.Enabled && current.Enabled() {
		if _, err := s.run(ctx, "systemctl", "--user", "disable", timer); err != nil {
			return fmt.Errorf("disable restored timer: %w", err)
		}
	}
	if snapshot.Active && !current.Active() {
		if _, err := s.run(ctx, "systemctl", "--user", "start", timer); err != nil {
			return fmt.Errorf("start restored timer: %w", err)
		}
	} else if !snapshot.Active && current.Active() {
		if _, err := s.run(ctx, "systemctl", "--user", "stop", timer); err != nil {
			return fmt.Errorf("stop restored timer: %w", err)
		}
	}
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return fmt.Errorf("verify restored timer state: %w", err)
	}
	if state.Enabled() != snapshot.Enabled {
		return fmt.Errorf("restored timer enabled state does not match the snapshot")
	}
	if state.Active() != snapshot.Active {
		return fmt.Errorf("restored timer active state does not match the snapshot")
	}
	return nil
}

func (s *Scheduler) Next(ctx context.Context, profileName string, _ config.Profile) (string, error) {
	_, timer, err := UnitNames(profileName)
	if err != nil {
		return "", err
	}
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return "", fmt.Errorf("query timer before next execution: %w", err)
	}
	if !state.Exists() {
		return "", fmt.Errorf("timer %s is not loaded", timer)
	}
	var propertyErr error
	for _, property := range []string{"NextElapseUSecRealtime", "NextElapseUSecMonotonic"} {
		output, err := s.run(
			ctx,
			"systemctl",
			"--user",
			"show",
			timer,
			"--property="+property,
			"--value",
		)
		if err != nil {
			propertyErr = err
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			continue
		}
		value := strings.TrimSpace(string(output))
		if value == "" || value == "n/a" {
			continue
		}
		if microseconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
			if property == "NextElapseUSecRealtime" {
				when := time.Unix(0, microseconds*int64(time.Microsecond)).Local()
				return when.Format(time.RFC3339), nil
			}
			continue
		}
		return value, nil
	}
	if propertyErr != nil {
		return "", fmt.Errorf("query next execution for timer %s: %w", timer, propertyErr)
	}
	return "", fmt.Errorf("timer %s has no next elapse time; inspect systemctl --user status %s", timer, timer)
}

func (s *Scheduler) Logs(ctx context.Context, profileName string, lines int) ([]byte, error) {
	service, _, err := UnitNames(profileName)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = 50
	}
	return s.run(ctx, "journalctl", "--user", "-u", service, "-n", strconv.Itoa(lines), "--no-pager")
}

func (s *Scheduler) ValidateCalendar(ctx context.Context, calendar string) error {
	if strings.TrimSpace(calendar) == "" || strings.ContainsAny(calendar, "\r\n\x00") {
		return errors.New("calendar schedule is empty or malformed")
	}
	_, err := s.run(ctx, "systemd-analyze", "calendar", calendar)
	if err == nil {
		return nil
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return nil
	}
	return fmt.Errorf("systemd rejected the calendar expression: %w", err)
}

func (s *Scheduler) Generate(profileName string, profile config.Profile) ([]byte, []byte, error) {
	service, _, err := UnitNames(profileName)
	if err != nil {
		return nil, nil, err
	}
	if profile.Schedule.Mode != "systemd" {
		return nil, nil, errors.New("schedule mode is not systemd")
	}
	calendar := strings.TrimSpace(profile.Schedule.OnCalendar)
	if calendar == "" {
		return nil, nil, errors.New("schedule calendar is required")
	}
	if profile.Schedule.Timezone != "" {
		calendar += " " + profile.Schedule.Timezone
	}

	execArgs := []string{
		"backup",
		"--profile=" + profileName,
		"--config=" + s.Paths.Config,
		"--credentials=" + s.Paths.Credentials,
		"--state-dir=" + s.Paths.StateDir,
	}
	var execParts []string
	execParts = append(execParts, quoteExecArg(s.BinaryPath))
	for _, arg := range execArgs {
		execParts = append(execParts, quoteExecArg(arg))
	}

	var serviceBuilder strings.Builder
	fmt.Fprintf(&serviceBuilder, `[Unit]
Description=FiveMesh Backup profile %s
Documentation=file:%s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ProtectClock=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
CapabilityBoundingSet=
`, profileName, s.Paths.Config, strings.Join(execParts, " "))
	writePaths := uniquePaths([]string{
		profile.Backup.Directory,
		s.Paths.StateDir,
	})
	for _, path := range writePaths {
		fmt.Fprintf(&serviceBuilder, "ReadWritePaths=%s\n", quoteExecArg(path))
	}

	var timerBuilder strings.Builder
	fmt.Fprintf(&timerBuilder, `[Unit]
Description=Schedule for FiveMesh Backup profile %s

[Timer]
Unit=%s
OnCalendar=%s
Persistent=%t
AccuracySec=1min
RandomizedDelaySec=0

[Install]
WantedBy=timers.target
`, profileName, service, calendar, profile.Schedule.Persistent)
	return []byte(serviceBuilder.String()), []byte(timerBuilder.String()), nil
}

func (s *Scheduler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	runner := s.Command
	if runner == nil {
		runner = ExecRunner{}
	}
	return runner.Run(ctx, name, args...)
}

func (s *Scheduler) queryTimerState(ctx context.Context, timer string) (timerState, error) {
	output, err := s.run(
		ctx,
		"systemctl",
		"--user",
		"show",
		timer,
		"--property=LoadState",
		"--property=ActiveState",
		"--property=UnitFileState",
	)
	if err != nil {
		return timerState{}, fmt.Errorf("systemd user manager unavailable or rejected timer state query: %w", err)
	}
	properties := make(map[string]string, 3)
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return timerState{}, fmt.Errorf("malformed systemctl state output for %s", timer)
		}
		if _, duplicate := properties[key]; duplicate {
			return timerState{}, fmt.Errorf("duplicate systemctl property %q for %s", key, timer)
		}
		properties[key] = strings.TrimSpace(value)
	}
	for _, required := range []string{"LoadState", "ActiveState", "UnitFileState"} {
		if _, ok := properties[required]; !ok {
			return timerState{}, fmt.Errorf("systemctl state output for %s is missing %s", timer, required)
		}
	}
	state := timerState{
		LoadState:     properties["LoadState"],
		ActiveState:   properties["ActiveState"],
		UnitFileState: properties["UnitFileState"],
	}
	if err := validateTimerState(timer, state); err != nil {
		return timerState{}, err
	}
	return state, nil
}

func validateTimerState(timer string, state timerState) error {
	switch state.LoadState {
	case "loaded", "not-found", "masked", "masked-runtime":
	case "error", "bad-setting":
		return fmt.Errorf("timer %s is in systemd error state %q", timer, state.LoadState)
	default:
		return fmt.Errorf("unexpected systemd LoadState %q for timer %s", state.LoadState, timer)
	}
	switch state.ActiveState {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing":
	default:
		return fmt.Errorf("unexpected systemd ActiveState %q for timer %s", state.ActiveState, timer)
	}
	switch state.UnitFileState {
	case "",
		"enabled", "enabled-runtime",
		"linked", "linked-runtime",
		"alias", "masked", "masked-runtime",
		"static", "disabled", "indirect", "generated", "transient":
	case "bad":
		return fmt.Errorf("timer %s has systemd UnitFileState \"bad\"", timer)
	default:
		return fmt.Errorf("unexpected systemd UnitFileState %q for timer %s", state.UnitFileState, timer)
	}
	if state.LoadState == "not-found" {
		if state.ActiveState == "active" {
			return fmt.Errorf("timer %s is not loaded but reports active state", timer)
		}
		if state.UnitFileState != "" {
			return fmt.Errorf("timer %s is not loaded but reports UnitFileState %q", timer, state.UnitFileState)
		}
	} else if state.UnitFileState == "" {
		return fmt.Errorf("timer %s is loaded but has no UnitFileState", timer)
	}
	return nil
}

func displayUnitFileState(state string) string {
	if state == "" {
		return "not-found"
	}
	return state
}

func (s *Scheduler) verifyActive(ctx context.Context, timer string) error {
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return fmt.Errorf("verify active timer %s: %w", timer, err)
	}
	if !state.Exists() {
		return fmt.Errorf("systemd did not load timer %s", timer)
	}
	if !state.Enabled() {
		return fmt.Errorf("systemd did not confirm timer %s as enabled (%s)", timer, state)
	}
	if !state.Active() {
		return fmt.Errorf("systemd did not confirm timer %s as active (%s)", timer, state)
	}
	return nil
}

func (s *Scheduler) verifyInactive(ctx context.Context, timer string) error {
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return fmt.Errorf("verify inactive timer %s: %w", timer, err)
	}
	if !state.SafelyInactive() {
		return fmt.Errorf("systemd did not confirm timer %s as disabled and inactive (%s)", timer, state)
	}
	return nil
}

func (s *Scheduler) disableAndVerify(ctx context.Context, timer string) error {
	state, err := s.queryTimerState(ctx, timer)
	if err != nil {
		return fmt.Errorf("cannot determine timer state before disable: %w", err)
	}
	if state.SafelyInactive() {
		return nil
	}
	_, commandErr := s.run(ctx, "systemctl", "--user", "disable", "--now", timer)
	after, verifyErr := s.queryTimerState(ctx, timer)
	if verifyErr != nil {
		if commandErr != nil {
			return fmt.Errorf("disable timer %s failed: %v; verification also failed: %w", timer, commandErr, verifyErr)
		}
		return fmt.Errorf("disable timer %s could not be verified: %w", timer, verifyErr)
	}
	if after.SafelyInactive() {
		return nil
	}
	if commandErr != nil {
		return fmt.Errorf("disable timer %s failed: %v; timer state is %s", timer, commandErr, after)
	}
	return fmt.Errorf("systemd did not confirm timer %s as disabled and inactive (%s)", timer, after)
}

func (s *Scheduler) verifyUnitFiles(ctx context.Context, service, timer []byte) error {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		return nil
	}
	directory, err := os.MkdirTemp("", "fivemesh-backup-systemd-*")
	if err != nil {
		return fmt.Errorf("create temporary unit validation directory: %w", err)
	}
	defer os.RemoveAll(directory)
	servicePath := filepath.Join(directory, "fivemesh-backup-validation.service")
	timerPath := filepath.Join(directory, "fivemesh-backup-validation.timer")
	if err := os.WriteFile(servicePath, service, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, timer, 0o600); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, analyze, "verify", servicePath, timerPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd rejected generated units: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Scheduler) verifyUnitDirectory() error {
	if err := os.MkdirAll(s.UnitDir, 0o755); err != nil {
		return fmt.Errorf("prepare systemd user unit directory: %w", err)
	}
	file, err := os.CreateTemp(s.UnitDir, ".fivemesh-backup-write-test-*")
	if err != nil {
		return fmt.Errorf("systemd user unit directory is not writable: %w", err)
	}
	name := file.Name()
	_ = file.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove systemd unit write test: %w", err)
	}
	return nil
}

func restoreUnit(path string, data []byte, exists bool) error {
	if !exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(path, data, 0o644)
}

func quoteExecArg(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	var result []string
	for _, path := range paths {
		path = filepath.Clean(path)
		if path == "." || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}
