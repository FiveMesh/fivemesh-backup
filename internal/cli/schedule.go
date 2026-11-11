package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
	"github.com/FiveMesh/fivemesh-backup/internal/recovery"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
	"golang.org/x/term"
)

var scheduleBuilder = buildScheduler

func runSchedule(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return withCode(exitUsage, errors.New("schedule requires a subcommand: status, configure, enable, update, disable, remove, next, logs, or run"))
	}
	subcommand := args[0]
	switch subcommand {
	case "status", "configure", "enable", "disable", "remove", "next", "logs", "run", "update":
	default:
		return withCode(exitUsage, fmt.Errorf("unknown schedule command %q", subcommand))
	}
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("schedule "+subcommand, stderr)
	paths := addPathFlags(flags, defaults)
	profileName := flags.String("profile", "", "profile name")
	lines := flags.Int("lines", 100, "journal lines to display")
	windowsPasswordPipe := flags.String("windows-password-pipe", "", "one-shot secure pipe for an unattended Windows task password")
	frequency := flags.String("frequency", "daily", "Windows schedule: daily, 6h, 12h, or custom")
	intervalMinutes := flags.Int("interval-minutes", 0, "custom interval in minutes; must divide 24 hours")
	startTime := flags.String("start-time", "06:00", "preferred local start time in HH:MM")
	logonMode := flags.String("logon-mode", config.WindowsLogonBackground, "Windows task logon: background (recommended), interactive, or password")
	persistentValue := flags.String("persistent", "true", "run once after a missed schedule: true or false")
	if err := parseFlags(flags, args[1:]); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("schedule does not accept positional arguments"))
	}
	cfg, err := loadConfig(paths)
	if err != nil {
		return err
	}
	scheduler, err := scheduleBuilder(pathValues(paths))
	if err != nil {
		return err
	}

	if subcommand == "status" {
		return printScheduleStatus(ctx, cfg, *profileName, scheduler, stdout)
	}
	resolvedName, profile, err := cfg.ResolveProfile(*profileName)
	if err != nil {
		return err
	}
	switch subcommand {
	case "configure":
		if scheduler.Mode() != config.ScheduleModeWindows {
			return errors.New("schedule configure is currently supported for Windows Task Scheduler only")
		}
		desired, configureErr := configureWindowsProfile(profile, *frequency, *intervalMinutes, *startTime, *logonMode, *persistentValue)
		if configureErr != nil {
			return withCode(exitUsage, configureErr)
		}
		snapshot, snapshotErr := scheduler.Snapshot(ctx, resolvedName)
		if snapshotErr != nil {
			return fmt.Errorf("snapshot current task state: %w", snapshotErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		secret, secretErr := logonSecretForTransition(ctx, stdin, stderr, scheduler, desired, snapshot, *windowsPasswordPipe)
		if secretErr != nil {
			return secretErr
		}
		defer clearLogonSecret(&secret)
		next, transitionErr := enableAndSaveSchedule(
			ctx,
			scheduler,
			cfg,
			*paths.config,
			resolvedName,
			desired,
			snapshot,
			secret,
			"configure schedule",
		)
		if transitionErr != nil {
			return transitionErr
		}
		fmt.Fprintf(stdout, "Windows schedule configured for %q.\nNext backup: %s\n", resolvedName, next)
		sendNotification(paths, notify.ConfigEvent(resolvedName, "Schedule configured", "Next backup: "+next, notify.Field{Name: "Schedule", Value: notify.ScheduleLabel(desired.Schedule), Inline: true}, notify.Field{Name: "Runs", Value: desired.Schedule.Windows.EffectiveLogonMode(), Inline: true}), stderr)
	case "enable", "update":
		if !profile.Schedule.IsAutomatic() {
			return errors.New("profile is configured for manual backups; run configure to choose a schedule")
		}
		if !scheduler.Manages(profile.Schedule.Mode) {
			return fmt.Errorf("profile is configured for %s scheduling, but the current backend is %s", profile.Schedule.Mode, scheduler.Mode())
		}
		snapshot, err := scheduler.Snapshot(ctx, resolvedName)
		if err != nil {
			return fmt.Errorf("snapshot current timer state: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		secret, err := logonSecretForTransition(ctx, stdin, stderr, scheduler, profile, snapshot, *windowsPasswordPipe)
		if err != nil {
			return err
		}
		defer clearLogonSecret(&secret)
		next, err := enableAndSaveSchedule(
			ctx,
			scheduler,
			cfg,
			*paths.config,
			resolvedName,
			profile,
			snapshot,
			secret,
			"enable",
		)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Schedule enabled for %q.\nNext backup: %s\n", resolvedName, next)
		sendNotification(paths, notify.ConfigEvent(resolvedName, "Schedule enabled", "Next backup: "+next, notify.Field{Name: "Schedule", Value: notify.ScheduleLabel(profile.Schedule), Inline: true}), stderr)
	case "disable":
		if !profile.Schedule.IsAutomatic() {
			return errors.New("profile is configured for manual backups")
		}
		if !scheduler.Manages(profile.Schedule.Mode) {
			return fmt.Errorf("profile is configured for %s scheduling, but the current backend is %s", profile.Schedule.Mode, scheduler.Mode())
		}
		snapshot, err := scheduler.Snapshot(ctx, resolvedName)
		if err != nil {
			return fmt.Errorf("snapshot current timer state: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		secret, err := logonSecretForTransition(ctx, stdin, stderr, scheduler, profile, snapshot, *windowsPasswordPipe)
		if err != nil {
			return err
		}
		defer clearLogonSecret(&secret)
		if err := scheduler.Disable(ctx, resolvedName); err != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("disable", err, restoreErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("disable cancelled", ctxErr, restoreErr)
		}
		profile.Schedule.Paused = true
		cfg.Profiles[resolvedName] = profile
		if err := config.SaveConfig(cfg, *paths.config); err != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("save paused state", err, restoreErr)
		}
		fmt.Fprintf(stdout, "Schedule disabled for %q. The schedule definition is preserved.\n", resolvedName)
		sendNotification(paths, notify.ConfigEvent(resolvedName, "Schedule paused", "Automatic backups are paused until the schedule is enabled again."), stderr)
	case "remove":
		snapshot, err := scheduler.Snapshot(ctx, resolvedName)
		if err != nil {
			return fmt.Errorf("snapshot current timer state: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		secret, err := logonSecretForTransition(ctx, stdin, stderr, scheduler, profile, snapshot, *windowsPasswordPipe)
		if err != nil {
			return err
		}
		defer clearLogonSecret(&secret)
		if err := scheduler.Remove(ctx, resolvedName); err != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("remove", err, restoreErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("removal cancelled", ctxErr, restoreErr)
		}
		profile.Schedule = config.Schedule{Mode: config.ScheduleModeManual}
		cfg.Profiles[resolvedName] = profile
		if err := config.SaveConfig(cfg, *paths.config); err != nil {
			restoreErr := restoreScheduleState(ctx, scheduler, resolvedName, snapshot, secret)
			return scheduleTransitionError("save manual state", err, restoreErr)
		}
		fmt.Fprintf(stdout, "Schedule removed for %q. Backups and user data were not deleted.\n", resolvedName)
		sendNotification(paths, notify.ConfigEvent(resolvedName, "Schedule removed", "This profile is now manual only. Existing backups were not deleted."), stderr)
	case "next":
		if !profile.Schedule.IsAutomatic() {
			return errors.New("profile is configured for manual backups")
		}
		if !scheduler.Manages(profile.Schedule.Mode) {
			return fmt.Errorf("profile is configured for %s scheduling, but the current backend is %s", profile.Schedule.Mode, scheduler.Mode())
		}
		if profile.Schedule.Paused {
			return errors.New("schedule is paused; enable it to inspect the next backup")
		}
		next, err := scheduler.Next(ctx, resolvedName, profile)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, next)
	case "logs":
		data, err := scheduler.Logs(ctx, resolvedName, *lines)
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	case "run":
		if !profile.Schedule.IsAutomatic() {
			return errors.New("profile is configured for manual backups; use \"fivemesh-backup backup\"")
		}
		if !scheduler.Manages(profile.Schedule.Mode) {
			return fmt.Errorf("profile is configured for %s scheduling, but the current backend is %s", profile.Schedule.Mode, scheduler.Mode())
		}
		fmt.Fprintf(stdout, "Starting scheduled backup for %q...\n", resolvedName)
		return scheduler.Run(ctx, resolvedName)
	}
	return nil
}

func configureWindowsProfile(
	profile config.Profile,
	frequency string,
	intervalMinutes int,
	startTime string,
	logonMode string,
	persistentValue string,
) (config.Profile, error) {
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	startTime = strings.TrimSpace(startTime)
	logonMode = strings.ToLower(strings.TrimSpace(logonMode))
	persistentValue = strings.TrimSpace(persistentValue)
	if frequency == "" {
		return config.Profile{}, errors.New("--frequency is required (daily, 6h, 12h, or custom)")
	}
	if startTime == "" {
		return config.Profile{}, errors.New("--start-time is required")
	}
	if !config.ValidWindowsLogonMode(logonMode) {
		return config.Profile{}, errors.New("--logon-mode must be background, interactive, or password")
	}
	if persistentValue == "" {
		return config.Profile{}, errors.New("--persistent is required (true or false)")
	}
	persistent, err := strconv.ParseBool(persistentValue)
	if err != nil {
		return config.Profile{}, errors.New("--persistent must be true or false")
	}
	windowsSchedule := config.WindowsSchedule{
		StartTime: startTime,
		LogonMode: logonMode,
	}
	switch frequency {
	case "daily":
		if intervalMinutes != 0 {
			return config.Profile{}, errors.New("--interval-minutes is only valid with --frequency custom")
		}
		windowsSchedule.Frequency = config.WindowsFrequencyDaily
	case "6h":
		if intervalMinutes != 0 {
			return config.Profile{}, errors.New("--interval-minutes is only valid with --frequency custom")
		}
		windowsSchedule.Frequency = config.WindowsFrequencyInterval
		windowsSchedule.IntervalMinutes = 6 * 60
	case "12h":
		if intervalMinutes != 0 {
			return config.Profile{}, errors.New("--interval-minutes is only valid with --frequency custom")
		}
		windowsSchedule.Frequency = config.WindowsFrequencyInterval
		windowsSchedule.IntervalMinutes = 12 * 60
	case "custom":
		if intervalMinutes == 0 {
			return config.Profile{}, errors.New("--interval-minutes is required with --frequency custom")
		}
		windowsSchedule.Frequency = config.WindowsFrequencyInterval
		windowsSchedule.IntervalMinutes = intervalMinutes
	default:
		return config.Profile{}, fmt.Errorf("unsupported --frequency %q; use daily, 6h, 12h, or custom", frequency)
	}
	if err := windowsSchedule.Validate(); err != nil {
		return config.Profile{}, err
	}
	profile.Schedule = config.Schedule{
		Mode:       config.ScheduleModeWindows,
		Persistent: persistent,
		Paused:     false,
		Windows:    windowsSchedule,
	}
	if err := profile.Validate(); err != nil {
		return config.Profile{}, fmt.Errorf("resulting profile is invalid: %w", err)
	}
	return profile, nil
}

func enableAndSaveSchedule(
	ctx context.Context,
	scheduler schedule.Manager,
	cfg config.Config,
	configPath string,
	profileName string,
	profile config.Profile,
	snapshot []byte,
	secret schedule.LogonSecret,
	operation string,
) (string, error) {
	next, err := scheduler.Enable(ctx, profileName, profile, secret)
	if err != nil {
		restoreErr := restoreScheduleState(ctx, scheduler, profileName, snapshot, secret)
		return "", scheduleTransitionError(operation, err, restoreErr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		restoreErr := restoreScheduleState(ctx, scheduler, profileName, snapshot, secret)
		return "", scheduleTransitionError(operation+" activation cancelled", ctxErr, restoreErr)
	}
	profile.Schedule.Paused = false
	cfg.Profiles[profileName] = profile
	if err := config.SaveConfig(cfg, configPath); err != nil {
		restoreErr := restoreScheduleState(ctx, scheduler, profileName, snapshot, secret)
		return "", scheduleTransitionError(operation+" save", err, restoreErr)
	}
	return next, nil
}

func printScheduleStatus(
	ctx context.Context,
	cfg config.Config,
	requested string,
	scheduler schedule.Manager,
	output io.Writer,
) error {
	names := make([]string, 0, len(cfg.Profiles))
	if requested != "" {
		name, _, err := cfg.ResolveProfile(requested)
		if err != nil {
			return err
		}
		names = append(names, name)
	} else {
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	stale := false
	for index, name := range names {
		profile := cfg.Profiles[name]
		status := scheduler.Status(ctx, name, profile)
		stale = stale || status.Stale
		if index > 0 {
			fmt.Fprintln(output)
		}
		if status.Mode != config.ScheduleModeSystemd && status.Mode != config.ScheduleModeWindows {
			fmt.Fprintf(output, "%s: %s (no automatic backups)\n", name, status.Mode)
		} else {
			backend := "systemd user timer"
			if status.Mode == config.ScheduleModeWindows {
				backend = "Windows Task Scheduler"
			}
			fmt.Fprintf(output, "%s: %s\n", name, status.Mode)
			state := fmt.Sprintf("enabled=%s active=%s", status.Enabled, status.Active)
			if profile.Schedule.Paused {
				state += " paused=true"
			}
			rows := [][2]string{{"scheduler", backend}, {"state", state}}
			if status.Next != "" {
				rows = append(rows, [2]string{"next run", status.Next})
			}
			lastRun := "never"
			if status.LastRun != "" {
				lastRun = status.LastRun
			}
			if status.LastResult != "" {
				lastRun += " (" + describeTaskResult(status.LastResult) + ")"
			}
			rows = append(rows, [2]string{"last run", lastRun})
			if status.LogonMode != "" {
				rows = append(rows, [2]string{"runs", status.LogonMode})
			}
			rows = append(rows, [2]string{status.ResourceKind, status.Resource})
			for _, row := range rows {
				fmt.Fprintf(output, "  %-10s %s\n", row[0], row[1])
			}
		}
		if status.UnitError != "" {
			fmt.Fprintf(output, "  warning: %s\n", status.UnitError)
		}
	}
	if stale {
		return errors.New("scheduling state does not match configuration")
	}
	return nil
}

// describeTaskResult explains common Task Scheduler result codes; other values
// (including systemd results) are shown unchanged.
func describeTaskResult(result string) string {
	switch strings.ToUpper(result) {
	case "0X00000000":
		return "succeeded"
	case "0X00041303":
		return "has not run yet"
	case "0X00041301":
		return "running now"
	case "0X800710E0":
		return "refused by Task Scheduler; the account may lack permission"
	default:
		return "result " + result
	}
}

func scheduleTransitionError(operation string, operationErr, restoreErr error) error {
	if restoreErr == nil {
		return fmt.Errorf("%s failed; previous timer state was restored: %w", operation, operationErr)
	}
	return fmt.Errorf(
		"%s failed and schedule rollback was incomplete: operation=%w rollback=%v; run \"fivemesh-backup schedule status\" and inspect the native scheduler (Linux: \"systemctl --user list-timers\"; Windows: \"Get-ScheduledTask\")",
		operation,
		operationErr,
		restoreErr,
	)
}

func restoreScheduleState(
	parent context.Context,
	scheduler schedule.Manager,
	profileName string,
	snapshot []byte,
	secret schedule.LogonSecret,
) error {
	recoveryCtx, cancel := recovery.Context(parent)
	defer cancel()
	return scheduler.Restore(recoveryCtx, profileName, snapshot, secret)
}

func logonSecretForTransition(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	scheduler schedule.Manager,
	profile config.Profile,
	snapshot []byte,
	pipeName string,
) (schedule.LogonSecret, error) {
	if scheduler.Mode() != config.ScheduleModeWindows ||
		(!schedule.NeedsWindowsPassword(profile) && !scheduler.SnapshotRequiresPassword(snapshot)) {
		return schedule.LogonSecret{}, nil
	}
	if pipeName != "" {
		password, err := readWindowsPasswordPipe(ctx, pipeName)
		if err != nil {
			return schedule.LogonSecret{}, fmt.Errorf("read Windows task password from secure pipe: %w", err)
		}
		return schedule.LogonSecret{WindowsPassword: password}, nil
	}
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return schedule.LogonSecret{}, errors.New("Windows task password must be entered from an interactive terminal")
	}
	fmt.Fprint(stdout, "Windows password for unattended Task Scheduler execution: ")
	value, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return schedule.LogonSecret{}, fmt.Errorf("read Windows task password: %w", err)
	}
	if len(value) == 0 {
		return schedule.LogonSecret{}, errors.New("Windows task password must not be empty")
	}
	return schedule.LogonSecret{WindowsPassword: string(value)}, nil
}

func clearLogonSecret(secret *schedule.LogonSecret) {
	secret.WindowsPassword = ""
}
