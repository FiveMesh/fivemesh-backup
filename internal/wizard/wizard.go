package wizard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/archive"
	"github.com/FiveMesh/fivemesh-backup/internal/backup"
	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/diskfree"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
	"github.com/FiveMesh/fivemesh-backup/internal/recovery"
	"github.com/FiveMesh/fivemesh-backup/internal/schedule"
)

type ScheduleManager = schedule.Lifecycle

type BackupFunc func(context.Context, string, config.Profile, string, func(string)) (backup.Outcome, error)

type Options struct {
	Paths         config.Paths
	StateDir      string
	Prompter      *Prompter
	AllowExisting bool

	DiscoverTools        func(context.Context, config.Database) (mysql.Tools, error)
	DiscoverDirectory    func(context.Context, config.Database, string) (mysql.Tools, error)
	TestConnection       func(context.Context, mysql.ToolInfo, config.Database, string) error
	ListDatabases        func(context.Context, mysql.ToolInfo, config.Database, string) ([]string, error)
	RunBackup            BackupFunc
	Scheduler            ScheduleManager
	WindowsPasswordInput func(context.Context) (string, error)
	// Notifications offers the Discord step and reports saved changes.
	// It is off by default so scripted callers and tests stay offline.
	Notifications    bool
	SendNotification SendFunc
	Now              func() time.Time
}

type Result struct {
	ProfileName    string
	ConfigCreated  bool
	Archive        string
	Verified       bool
	ArchiveSize    int64
	Duration       time.Duration
	NextScheduled  string
	BackupExecuted bool
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func Run(ctx context.Context, options Options) (Result, error) {
	if options.Prompter == nil {
		return Result{}, errors.New("interactive prompter is required")
	}
	if options.DiscoverTools == nil {
		options.DiscoverTools = mysql.Discover
	}
	if options.DiscoverDirectory == nil {
		options.DiscoverDirectory = mysql.DiscoverInDirectory
	}
	if options.TestConnection == nil {
		options.TestConnection = mysql.TestConnection
	}
	if options.ListDatabases == nil {
		options.ListDatabases = defaultListDatabases
	}
	if options.StateDir == "" {
		options.StateDir = options.Paths.StateDir
	}
	if options.RunBackup == nil {
		options.RunBackup = func(
			runContext context.Context,
			name string,
			profile config.Profile,
			password string,
			onPhase func(string),
		) (backup.Outcome, error) {
			return backup.New(name, profile, password, options.StateDir).Run(runContext, backup.Options{OnPhase: onPhase})
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	prompt := options.Prompter
	prompt.Intro()

	cfg, exists, err := loadExisting(options.Paths.Config)
	if err != nil {
		return Result{}, err
	}
	if exists && !options.AllowExisting {
		return Result{}, fmt.Errorf("configuration already exists at %s; use \"fivemesh-backup configure\"", options.Paths.Config)
	}
	if cfg.Profiles == nil {
		cfg = config.Config{
			Version:  config.CurrentVersion,
			Profiles: make(map[string]config.Profile),
		}
	}

	profileName, previous, existingProfile, err := chooseProfile(prompt, cfg, exists)
	if err != nil {
		return Result{}, err
	}
	existingPassword := ""
	if existingProfile {
		existingPassword, _ = config.LoadCredential(options.Paths.Credentials, profileName)
	}

	prompt.Step(1, "Database")
	profile, password, err := configureDatabase(ctx, prompt, options, previous, existingPassword)
	if err != nil {
		return Result{}, err
	}

	prompt.Step(2, "Storage")
	if err := configureStorage(prompt, &profile); err != nil {
		return Result{}, err
	}

	prompt.Step(3, "Schedule")
	if err := configureSchedule(ctx, prompt, profileName, &profile, options.Scheduler); err != nil {
		return Result{}, err
	}

	profile.Schedule.Mode = strings.TrimSpace(profile.Schedule.Mode)
	if profile.Schedule.Mode == "" {
		profile.Schedule.Mode = config.ScheduleModeManual
	}
	profile.Schedule.Paused = false
	if err := profile.Validate(); err != nil {
		return Result{}, fmt.Errorf("configuration is invalid: %w", err)
	}
	previousConfig := cloneConfig(cfg)
	previousCredentials := config.CredentialFile{Profiles: make(map[string]config.Credential)}
	credentialsExisted := false
	if exists {
		if _, statErr := os.Stat(options.Paths.Credentials); statErr == nil {
			previousCredentials, err = config.LoadCredentials(options.Paths.Credentials)
			if err != nil {
				return Result{}, fmt.Errorf("existing credentials cannot be updated safely: %w", err)
			}
			credentialsExisted = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return Result{}, fmt.Errorf("inspect existing credentials: %w", statErr)
		}
	}
	cfg.Profiles[profileName] = profile
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = profileName
	}
	credentials := cloneCredentials(previousCredentials)
	credentials.Profiles[profileName] = config.Credential{Password: password}

	if options.Notifications {
		discord := cfg.Notifications.Discord
		if discord.Enabled && credentials.Notifications.DiscordWebhookURL != "" {
			prompt.Note("Discord notifications are on (" + NotificationSummary(discord) + "). Change them with: fivemesh-backup notify discord")
		} else {
			want, confirmErr := prompt.Confirm("Post backup results to a Discord channel?", false)
			if confirmErr != nil {
				return Result{}, confirmErr
			}
			if want {
				setup, setupErr := ConfigureDiscord(ctx, prompt, discord, "", options.SendNotification)
				switch {
				case errors.Is(setupErr, ErrSkipped):
					prompt.Note("Skipped. You can set it up later with: fivemesh-backup notify discord")
				case setupErr != nil:
					return Result{}, setupErr
				default:
					cfg.Notifications.Discord = setup.Settings
					credentials.Notifications.DiscordWebhookURL = setup.WebhookURL
				}
			}
		}
	}

	prompt.Step(4, "Review")
	notificationLabel := ""
	if options.Notifications {
		notificationLabel = NotificationSummary(cfg.Notifications.Discord)
	}
	printSummary(prompt, profileName, profile, options.Paths, notificationLabel)
	apply, err := prompt.Confirm("Save this configuration?", true)
	if err != nil {
		return Result{}, err
	}
	if !apply {
		return Result{}, ErrCancelled
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if profile.Schedule.IsAutomatic() && options.Scheduler != nil && !options.Scheduler.Manages(profile.Schedule.Mode) {
		return Result{}, fmt.Errorf(
			"profile is configured for %s scheduling, but the current backend is %s",
			profile.Schedule.Mode,
			options.Scheduler.Mode(),
		)
	}
	scheduleChanged := options.Scheduler != nil &&
		(profile.Schedule.IsAutomatic() || previous.Schedule.IsAutomatic())
	if options.Scheduler != nil && !scheduleChanged && profile.Schedule.Mode == config.ScheduleModeManual {
		stale, detail, inspectErr := options.Scheduler.Inspect(ctx, profileName, profile)
		if inspectErr != nil {
			return Result{}, fmt.Errorf("could not verify existing timer state; no configuration changes were saved: %w", inspectErr)
		}
		if stale {
			scheduleChanged = true
			prompt.Info("Existing timer state was detected for a manual profile and will be removed.")
			if detail != "" {
				prompt.Info(detail)
			}
		}
	}
	var scheduleSnapshot []byte
	var scheduleSecret schedule.LogonSecret
	defer func() {
		scheduleSecret.WindowsPassword = ""
	}()
	if scheduleChanged {
		if profile.Schedule.IsAutomatic() {
			if err := options.Scheduler.Validate(ctx, profileName, profile); err != nil {
				return Result{}, fmt.Errorf("schedule validation failed; no configuration changes were saved: %w", err)
			}
		}
		scheduleSnapshot, err = options.Scheduler.Snapshot(ctx, profileName)
		if err != nil {
			return Result{}, fmt.Errorf("could not snapshot the existing schedule; no configuration changes were saved: %w", err)
		}
		if options.Scheduler.Mode() == config.ScheduleModeWindows &&
			(schedule.NeedsWindowsPassword(profile) || options.Scheduler.SnapshotRequiresPassword(scheduleSnapshot)) {
			if options.WindowsPasswordInput != nil {
				scheduleSecret.WindowsPassword, err = options.WindowsPasswordInput(ctx)
				if err != nil {
					return Result{}, fmt.Errorf("read Windows task password from secure input: %w", err)
				}
			} else {
				scheduleSecret.WindowsPassword, err = prompt.SecureSecret("Windows password for unattended Task Scheduler execution")
				if err != nil {
					return Result{}, err
				}
				confirmation, confirmErr := prompt.SecureSecret("Confirm Windows password")
				if confirmErr != nil {
					return Result{}, confirmErr
				}
				if scheduleSecret.WindowsPassword != confirmation {
					return Result{}, errors.New("Windows passwords do not match")
				}
			}
		}
		if err := options.Scheduler.Disable(ctx, profileName); err != nil {
			restoreErr := restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret)
			if restoreErr != nil {
				return Result{}, fmt.Errorf(
					"could not pause the existing schedule and could not restore it: pause=%v restore=%v",
					err,
					restoreErr,
				)
			}
			return Result{}, fmt.Errorf("could not pause the existing schedule; no configuration changes were saved: %w", err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			restoreErr := restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret)
			if restoreErr != nil {
				return Result{}, fmt.Errorf(
					"operation was cancelled after pausing the timer and restoration failed: %w",
					restoreErr,
				)
			}
			return Result{}, ctxErr
		}
	}
	if exists {
		err = config.SaveReconfigured(cfg, credentials, options.Paths)
	} else {
		err = config.SaveInitial(cfg, credentials, options.Paths)
	}
	if err != nil {
		if scheduleChanged {
			if restoreErr := restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret); restoreErr != nil {
				return Result{}, fmt.Errorf("configuration save failed and the previous schedule could not be restored: save=%v restore=%v", err, restoreErr)
			}
		}
		return Result{}, err
	}
	result := Result{ProfileName: profileName, ConfigCreated: !exists}
	if ctxErr := ctx.Err(); ctxErr != nil {
		configRollbackErr := rollbackConfiguration(
			options.Paths, exists, previousConfig, previousCredentials, credentialsExisted,
		)
		var scheduleRollbackErr error
		if scheduleChanged {
			scheduleRollbackErr = restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret)
		}
		return result, schedulingRollbackError(ctxErr, configRollbackErr, scheduleRollbackErr)
	}
	if scheduleChanged {
		if profile.Schedule.IsAutomatic() {
			if profile.Schedule.Mode == config.ScheduleModeWindows &&
				profile.Schedule.Windows.EffectiveLogonMode() == config.WindowsLogonBackground {
				prompt.Note("Windows may ask for administrator approval once to register the background task.")
			}
			next, scheduleErr := options.Scheduler.Enable(ctx, profileName, profile, scheduleSecret)
			if scheduleErr != nil {
				configRollbackErr := rollbackConfiguration(
					options.Paths, exists, previousConfig, previousCredentials, credentialsExisted,
				)
				scheduleRollbackErr := restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret)
				return result, schedulingRollbackError(scheduleErr, configRollbackErr, scheduleRollbackErr)
			}
			result.NextScheduled = next
		} else {
			if removeErr := options.Scheduler.Remove(ctx, profileName); removeErr != nil {
				configRollbackErr := rollbackConfiguration(
					options.Paths, exists, previousConfig, previousCredentials, credentialsExisted,
				)
				scheduleRollbackErr := restoreScheduleState(ctx, options.Scheduler, profileName, scheduleSnapshot, scheduleSecret)
				return result, schedulingRollbackError(removeErr, configRollbackErr, scheduleRollbackErr)
			}
			result.NextScheduled = "Manual only"
		}
	} else {
		result.NextScheduled = "Manual only"
	}
	if scheduleChanged && result.NextScheduled != "Manual only" {
		prompt.Success("Configuration saved and automatic backups are active.")
	} else if scheduleChanged {
		prompt.Success("Configuration saved and the previous schedule was removed.")
	} else {
		prompt.Success("Configuration saved.")
	}
	if options.Notifications {
		title := "Profile updated"
		if !existingProfile {
			title = "Profile created"
		}
		notifySaved(ctx, prompt, options.SendNotification, cfg, credentials,
			notify.ConfigEvent(profileName, title, "Saved with the setup wizard.", notify.ProfileFields(profile)...))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}

	runBackup, err := prompt.Confirm("Run a test backup now?", true)
	if err != nil {
		return result, err
	}
	if runBackup {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		result.BackupExecuted = true
		outcome, backupErr := options.RunBackup(ctx, profileName, profile, password, func(phase string) {
			prompt.Note("  " + strings.ReplaceAll(phase, "-", " "))
		})
		if backupErr != nil {
			return result, fmt.Errorf("configuration was saved, but the test backup failed: %w", backupErr)
		}
		verification, verifyErr := archive.Verify(outcome.Record.Archive)
		if verifyErr != nil {
			return result, fmt.Errorf("configuration was saved, but test archive verification failed: %w", verifyErr)
		}
		result.Archive = outcome.Record.Archive
		result.Verified = true
		result.ArchiveSize = verification.Metadata.SizeBytes
		result.Duration = time.Duration(outcome.Record.DurationMS) * time.Millisecond
		prompt.Success(fmt.Sprintf(
			"Backup verified: %s compressed in %s.",
			formatBytes(result.ArchiveSize),
			result.Duration.Round(time.Millisecond),
		))
		prompt.Note("  " + result.Archive)
	}

	if result.NextScheduled == "Manual only" {
		prompt.Info("Next scheduled backup: manual only")
	} else {
		prompt.Info("Next scheduled backup: " + result.NextScheduled)
	}
	printNextSteps(prompt, profileName, profile)
	return result, nil
}

func printNextSteps(prompt *Prompter, profileName string, profile config.Profile) {
	flag := ""
	if profileName != "" {
		flag = " --profile " + profileName
	}
	prompt.Info("")
	prompt.Info("Useful commands:")
	rows := [][2]string{
		{"fivemesh-backup backup" + flag, "run a backup now"},
		{"fivemesh-backup list" + flag, "show saved archives"},
		{"fivemesh-backup status", "latest result for every profile"},
	}
	if profile.Schedule.IsAutomatic() {
		rows = append(rows, [2]string{"fivemesh-backup schedule status", "check automatic backups"})
	}
	rows = append(rows, [2]string{"fivemesh-backup configure", "change these settings later"})
	for _, row := range rows {
		prompt.Note(fmt.Sprintf("  %-44s %s", row[0], row[1]))
	}
}

func chooseProfile(prompt *Prompter, cfg config.Config, configExists bool) (string, config.Profile, bool, error) {
	if !configExists || len(cfg.Profiles) == 0 {
		name, err := prompt.Text("Profile name", "production", validateNewProfileName(nil))
		return name, config.Profile{}, false, err
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	defaultIndex := 0
	choices := make([]Choice, 0, len(names)+1)
	for index, name := range names {
		choices = append(choices, Choice{Label: name})
		if name == cfg.DefaultProfile {
			defaultIndex = index
		}
	}
	choices = append(choices, Choice{Label: "Create a new profile"})
	index, err := prompt.Select("Profile to configure", choices, defaultIndex)
	if err != nil {
		return "", config.Profile{}, false, err
	}
	if index == len(names) {
		name, err := prompt.Text("New profile name", "", validateNewProfileName(cfg.Profiles))
		return name, config.Profile{}, false, err
	}
	name := names[index]
	return name, cfg.Profiles[name], true, nil
}

func configureDatabase(
	ctx context.Context,
	prompt *Prompter,
	options Options,
	previous config.Profile,
	existingPassword string,
) (config.Profile, string, error) {
	if options.ListDatabases == nil {
		options.ListDatabases = defaultListDatabases
	}
	profile := previous
	engineIndex := 0
	if profile.Database.Flavor == "mysql" {
		engineIndex = 1
	}
	engineIndex, err := prompt.Select("Database engine", []Choice{
		{Label: "MariaDB"},
		{Label: "MySQL"},
	}, engineIndex)
	if err != nil {
		return profile, "", err
	}
	flavor := "mariadb"
	if engineIndex == 1 {
		flavor = "mysql"
	}
	database := config.Database{
		Engine:            "mysql",
		Flavor:            flavor,
		DumpTool:          "auto",
		ClientTool:        "auto",
		SingleTransaction: true,
		IncludeRoutines:   true,
		IncludeEvents:     true,
	}
	if previous.Database.Engine != "" {
		database.DumpTool = previous.Database.DumpTool
		database.ClientTool = previous.Database.ClientTool
		database.SingleTransaction = previous.Database.SingleTransaction
		database.IncludeRoutines = previous.Database.IncludeRoutines
		database.IncludeEvents = previous.Database.IncludeEvents
	}
	tools, err := options.DiscoverTools(ctx, database)
	if err != nil {
		prompt.Warn(fmt.Sprintf("Could not find the MariaDB/MySQL client tools automatically: %v", err))
		prompt.Note("Enter the MariaDB/MySQL installation folder or its bin folder (for example C:\\Program Files\\MariaDB 12.2 or C:\\xampp\\mysql).")
		prompt.Note("Leave it empty to cancel setup; nothing will be saved.")
		for {
			directory, inputErr := prompt.Text("Client installation directory", "", nil)
			if inputErr != nil {
				return profile, "", inputErr
			}
			if directory == "" {
				return profile, "", ErrCancelled
			}
			tools, err = options.DiscoverDirectory(ctx, database, directory)
			if err == nil {
				break
			}
			prompt.Fail(fmt.Sprintf("No usable client tools in that folder: %v", err))
			retry, confirmErr := prompt.Confirm("Try another installation directory?", true)
			if confirmErr != nil {
				return profile, "", confirmErr
			}
			if !retry {
				return profile, "", ErrCancelled
			}
		}
	}
	prompt.Success(fmt.Sprintf("Found %s %s and %s",
		filepath.Base(tools.Dump.Path), shortVersion(tools.Dump.Version),
		filepath.Base(tools.Client.Path)))
	prompt.Note("  " + filepath.Dir(tools.Dump.Path))
	database.DumpTool = tools.Dump.Path
	database.ClientTool = tools.Client.Path

	for {
		connectionIndex := 1
		if previous.Database.Socket != "" {
			connectionIndex = 0
		}
		socketChoice := Choice{Label: "Local socket", Description: "database on this machine, via its Unix socket"}
		if runtime.GOOS == "windows" {
			socketChoice = Choice{Label: "Named pipe", Description: "only if the server enables named pipes"}
		}
		connectionIndex, err = prompt.Select("How do you connect to the database?", []Choice{
			socketChoice,
			{Label: "TCP", Description: "host and port; works for local and remote servers"},
		}, connectionIndex)
		if err != nil {
			return profile, "", err
		}
		database.Host = ""
		database.Socket = ""
		database.Port = 0
		if connectionIndex == 0 {
			defaultSocket := previous.Database.Socket
			if defaultSocket == "" {
				defaultSocket = detectSocket()
			}
			database.Socket, err = prompt.Text("Socket path", defaultSocket, func(value string) error {
				if value == "" {
					return errors.New("socket path is required")
				}
				return nil
			})
		} else {
			hostDefault := previous.Database.Host
			if hostDefault == "" {
				hostDefault = "127.0.0.1"
			}
			database.Host, err = prompt.Text("Host", hostDefault, requireNonempty("host"))
			if err == nil {
				portDefault := previous.Database.Port
				if portDefault == 0 {
					portDefault = 3306
				}
				database.Port, err = prompt.Integer("Port", portDefault, 1, 65535)
			}
		}
		if err != nil {
			return profile, "", err
		}
		database.Username, err = prompt.Text("Database username", defaultString(previous.Database.Username, "root"), requireNonempty("username"))
		if err != nil {
			return profile, "", err
		}
		password := existingPassword
		passwordLabel := "Database password"
		if existingPassword != "" {
			passwordLabel += " (press Enter to keep the current one)"
		}
		entered, err := prompt.Secret(passwordLabel)
		if err != nil {
			return profile, "", err
		}
		if entered != "" {
			password = entered
		} else if password == "" {
			useEmpty, err := prompt.Confirm("This account has no password. Continue without one?", false)
			if err != nil {
				return profile, "", err
			}
			if !useEmpty {
				continue
			}
		}

		var available []string
		var listErr error
		taskErr := prompt.Task(ctx, "Connecting to "+describeEndpoint(database)+"...", func(runContext context.Context) error {
			available, listErr = options.ListDatabases(runContext, tools.Client, database, password)
			return nil
		})
		if taskErr != nil {
			return profile, "", taskErr
		}
		var commandErr *mysql.CommandError
		if errors.As(listErr, &commandErr) {
			retry, promptErr := reviewConnectionFailure(prompt, listErr)
			if promptErr != nil {
				return profile, "", promptErr
			}
			if !retry {
				return profile, "", listErr
			}
			previous.Database = database
			existingPassword = password
			continue
		}
		database.Database, err = chooseDatabase(prompt, available, previous.Database.Database)
		if err != nil {
			return profile, "", err
		}

		var testErr error
		taskErr = prompt.Task(ctx, "Checking access to "+database.Database+"...", func(runContext context.Context) error {
			testErr = options.TestConnection(runContext, tools.Client, database, password)
			return nil
		})
		if taskErr != nil {
			return profile, "", taskErr
		}
		if testErr != nil {
			retry, promptErr := reviewConnectionFailure(prompt, testErr)
			if promptErr != nil {
				return profile, "", promptErr
			}
			if !retry {
				return profile, "", testErr
			}
			previous.Database = database
			existingPassword = password
			continue
		}
		prompt.Success(fmt.Sprintf("Connected to %s as %s", database.Database, database.Username))
		profile.Database = database
		return profile, password, nil
	}
}

func reviewConnectionFailure(prompt *Prompter, err error) (bool, error) {
	prompt.Fail(actionableConnectionError(err))
	return prompt.Confirm("Review connection details?", true)
}

// chooseDatabase offers the databases the account can see. When listing was
// not possible, it falls back to typing the name.
func chooseDatabase(prompt *Prompter, available []string, previous string) (string, error) {
	validate := func(value string) error {
		if value == "" {
			return errors.New("database name is required")
		}
		if strings.HasPrefix(value, "-") {
			return errors.New("database name must not start with '-'")
		}
		return nil
	}
	if len(available) == 0 {
		return prompt.Text("Database name", previous, validate)
	}
	choices := make([]Choice, 0, len(available)+1)
	defaultIndex := 0
	for index, name := range available {
		choices = append(choices, Choice{Label: name})
		if name == previous {
			defaultIndex = index
		}
	}
	choices = append(choices, Choice{Label: "Other...", Description: "type a database name"})
	index, err := prompt.Select("Which database should be backed up?", choices, defaultIndex)
	if err != nil {
		return "", err
	}
	if index < len(available) {
		return available[index], nil
	}
	return prompt.Text("Database name", previous, validate)
}

func describeEndpoint(database config.Database) string {
	if database.Socket != "" {
		return database.Socket
	}
	return net.JoinHostPort(database.Host, strconv.Itoa(database.Port))
}

// clientRelease matches the release in client banners such as
// `C:\Program Files\MariaDB 12.2\bin\mariadb-dump.exe from 12.2.2-MariaDB, client 10.19`
// and `mysqldump  Ver 8.0.36 for Win64`. Installation paths may contain
// version-like text, so only the token after "from" or "Ver" counts.
var clientRelease = regexp.MustCompile(`\b(?:from|Ver|Distrib)\s+(\d+\.\d+[0-9A-Za-z.\-]*)`)

// shortVersion returns the release number from a client version banner, or
// the banner unchanged when it has no recognizable release.
func shortVersion(version string) string {
	if match := clientRelease.FindStringSubmatch(version); match != nil {
		return match[1]
	}
	return version
}

// compressionPresetIndex maps a saved level to the compression menu so that
// reconfiguration preselects the current choice. Unset means Balanced.
func compressionPresetIndex(level int) int {
	switch level {
	case 0, 3:
		return 1
	case 1:
		return 0
	case 9:
		return 2
	default:
		return 3
	}
}

func configureStorage(prompt *Prompter, profile *config.Profile) error {
	defaultDirectory := profile.Backup.Directory
	if defaultDirectory == "" {
		home, _ := os.UserHomeDir()
		defaultDirectory = filepath.Join(home, "fivemesh-backups")
	}
	for {
		directory, err := prompt.Text("Where should backups be saved?", defaultDirectory, nil)
		if err != nil {
			return err
		}
		if err := validateWritableDirectory(directory); err != nil {
			prompt.Fail("Cannot use that folder: " + err.Error())
			continue
		}
		available, availableErr := diskfree.AvailableBytes(directory)
		if availableErr == nil {
			prompt.Note("  " + formatBytes(int64(available)) + " free on this drive")
		}
		profile.Backup.Directory = directory
		break
	}

	compressionIndex, err := prompt.Select("Compression", []Choice{
		{Label: "Fast", Description: "Zstandard level 1"},
		{Label: "Balanced", Description: "Zstandard level 3, recommended"},
		{Label: "Smaller archives", Description: "Zstandard level 9, uses more CPU"},
		{Label: "Custom level"},
	}, compressionPresetIndex(profile.Backup.CompressionLevel))
	if err != nil {
		return err
	}
	levels := []int{1, 3, 9, 0}
	profile.Backup.CompressionLevel = levels[compressionIndex]
	if profile.Backup.CompressionLevel == 0 {
		profile.Backup.CompressionLevel, err = prompt.Integer("Compression level", 3, 1, 19)
		if err != nil {
			return err
		}
	}

	retentionDefault := 0
	switch {
	case profile.Retention.KeepLast == 0 && profile.Retention.KeepDays == 0:
		retentionDefault = 0
	case profile.Retention.KeepLast == 14 && profile.Retention.KeepDays == 30:
		retentionDefault = 1
	case profile.Retention.KeepLast == 30 && profile.Retention.KeepDays == 90:
		retentionDefault = 2
	case profile.Retention.KeepLast != 7 || profile.Retention.KeepDays != 30:
		retentionDefault = 3
	}
	retentionIndex, err := prompt.Select("How long should backups be kept?", []Choice{
		{Label: "30 days", Description: "and always the newest 7, recommended"},
		{Label: "30 days", Description: "and always the newest 14"},
		{Label: "90 days", Description: "and always the newest 30"},
		{Label: "Custom"},
	}, retentionDefault)
	if err != nil {
		return err
	}
	switch retentionIndex {
	case 0:
		profile.Retention = config.Retention{KeepLast: 7, KeepDays: 30}
	case 1:
		profile.Retention = config.Retention{KeepLast: 14, KeepDays: 30}
	case 2:
		profile.Retention = config.Retention{KeepLast: 30, KeepDays: 90}
	case 3:
		profile.Retention.KeepLast, err = prompt.Integer("Keep the newest N backups", defaultInt(profile.Retention.KeepLast, 7), 1, 10000)
		if err == nil {
			profile.Retention.KeepDays, err = prompt.Integer("Keep backups for N days", defaultInt(profile.Retention.KeepDays, 30), 0, 100000)
		}
		if err != nil {
			return err
		}
	}
	minimumMiB := defaultInt64(profile.Backup.MinimumFreeSpaceBytes/(1024*1024), 512)
	minimumMiB, err = prompt.Int64("Skip the backup when free disk space is below (MiB)", minimumMiB, 0)
	if err != nil {
		return err
	}
	profile.Backup.MinimumFreeSpaceBytes = minimumMiB * 1024 * 1024
	return nil
}

func configureSchedule(
	ctx context.Context,
	prompt *Prompter,
	profileName string,
	profile *config.Profile,
	scheduler ScheduleManager,
) error {
	if scheduler == nil {
		profile.Schedule = config.Schedule{Mode: config.ScheduleModeManual}
		prompt.Info("Automatic scheduling is not available on this platform.")
		return nil
	}
	if scheduler.Mode() == config.ScheduleModeWindows {
		return configureWindowsSchedule(ctx, prompt, profileName, profile, scheduler)
	}
	if scheduler.Mode() != config.ScheduleModeSystemd {
		return fmt.Errorf("unsupported scheduler backend %q", scheduler.Mode())
	}
	defaultIndex := 3
	switch {
	case profile.Schedule.Mode == config.ScheduleModeManual:
		defaultIndex = 0
	case profile.Schedule.OnCalendar != "":
		defaultIndex = 4
	}
	index, err := prompt.Select("Automatic backups", []Choice{
		{Label: "Manual only"},
		{Label: "Every 6 hours"},
		{Label: "Every 12 hours"},
		{Label: "Daily", Description: "Recommended; default 06:00 local time"},
		{Label: "Custom systemd calendar"},
	}, defaultIndex)
	if err != nil {
		return err
	}
	if index == 0 {
		profile.Schedule = config.Schedule{Mode: config.ScheduleModeManual}
		return nil
	}
	timezoneDefault := profile.Schedule.Timezone
	if timezoneDefault == "" {
		timezoneDefault = localTimezone()
	}
	timezone, err := prompt.Text("Timezone (IANA name)", timezoneDefault, func(value string) error {
		if value == "" {
			return errors.New("timezone is required")
		}
		if _, err := time.LoadLocation(value); err != nil {
			return errors.New("use an IANA timezone such as Europe/Brussels")
		}
		return nil
	})
	if err != nil {
		return err
	}
	profile.Schedule.Mode = config.ScheduleModeSystemd
	profile.Schedule.Timezone = timezone
	profile.Schedule.Windows = config.WindowsSchedule{}
	switch index {
	case 1, 2, 3:
		clock, err := prompt.Text("Preferred start time (HH:MM)", "06:00", validateClock)
		if err != nil {
			return err
		}
		interval := 24
		if index == 1 {
			interval = 6
		} else if index == 2 {
			interval = 12
		}
		profile.Schedule.OnCalendar = calendarForInterval(interval, clock)
	case 4:
		profile.Schedule.OnCalendar, err = prompt.Text(
			"systemd OnCalendar expression",
			defaultString(profile.Schedule.OnCalendar, "*-*-* 06:00:00"),
			func(value string) error {
				if value == "" || strings.ContainsAny(value, "\r\n\x00") {
					return errors.New("enter a single valid calendar expression")
				}
				return nil
			},
		)
		if err != nil {
			return err
		}
	}
	if err := scheduler.Validate(ctx, profileName, *profile); err != nil {
		return err
	}
	persistent, err := prompt.Confirm(
		"Run once after a missed schedule (for example after shutdown)?",
		true,
	)
	if err != nil {
		return err
	}
	profile.Schedule.Persistent = persistent
	prompt.Info("The timer uses local calendar time and handles daylight-saving transitions through systemd.")
	if persistent {
		prompt.Info("A missed run is caught up once; it is not replayed repeatedly. The backup lock prevents overlap.")
	} else {
		prompt.Info("Missed runs are skipped. The backup lock still prevents overlap.")
	}
	return nil
}

func configureWindowsSchedule(
	ctx context.Context,
	prompt *Prompter,
	profileName string,
	profile *config.Profile,
	scheduler ScheduleManager,
) error {
	defaultIndex := 3
	switch {
	case profile.Schedule.Mode == config.ScheduleModeManual:
		defaultIndex = 0
	case profile.Schedule.Mode == config.ScheduleModeWindows:
		switch profile.Schedule.Windows.IntervalMinutes {
		case 360:
			defaultIndex = 1
		case 720:
			defaultIndex = 2
		case 0:
			defaultIndex = 3
		default:
			defaultIndex = 4
		}
	}
	index, err := prompt.Select("Automatic backups", []Choice{
		{Label: "Manual only"},
		{Label: "Every 6 hours"},
		{Label: "Every 12 hours"},
		{Label: "Daily", Description: "Recommended; default 06:00 local time"},
		{Label: "Custom interval"},
	}, defaultIndex)
	if err != nil {
		return err
	}
	if index == 0 {
		profile.Schedule = config.Schedule{Mode: config.ScheduleModeManual}
		return nil
	}

	previousStart := ""
	previousInterval := 0
	previousLogonMode := config.WindowsLogonBackground
	if profile.Schedule.Mode == config.ScheduleModeWindows {
		previousStart = profile.Schedule.Windows.StartTime
		previousInterval = profile.Schedule.Windows.IntervalMinutes
		previousLogonMode = profile.Schedule.Windows.EffectiveLogonMode()
	}
	profile.Schedule.Mode = config.ScheduleModeWindows
	profile.Schedule.OnCalendar = ""
	profile.Schedule.Timezone = ""
	profile.Schedule.Windows = config.WindowsSchedule{
		Frequency: config.WindowsFrequencyDaily,
		StartTime: defaultString(previousStart, "06:00"),
	}
	clockDefault := profile.Schedule.Windows.StartTime
	if clockDefault == "" {
		clockDefault = "06:00"
	}
	clock, err := prompt.Text("Preferred start time (HH:MM)", clockDefault, func(value string) error {
		_, _, err := config.ParseClock(value)
		return err
	})
	if err != nil {
		return err
	}
	profile.Schedule.Windows.StartTime = clock
	switch index {
	case 1:
		profile.Schedule.Windows.Frequency = config.WindowsFrequencyInterval
		profile.Schedule.Windows.IntervalMinutes = 360
	case 2:
		profile.Schedule.Windows.Frequency = config.WindowsFrequencyInterval
		profile.Schedule.Windows.IntervalMinutes = 720
	case 3:
		profile.Schedule.Windows.Frequency = config.WindowsFrequencyDaily
		profile.Schedule.Windows.IntervalMinutes = 0
	case 4:
		defaultHours := 8
		if previousInterval >= 60 {
			defaultHours = previousInterval / 60
		}
		for {
			hours, inputErr := prompt.Integer("Custom interval in hours (1-24, divide 24 evenly)", defaultHours, 1, 24)
			if inputErr != nil {
				return inputErr
			}
			if 24%hours == 0 {
				profile.Schedule.Windows.Frequency = config.WindowsFrequencyInterval
				profile.Schedule.Windows.IntervalMinutes = hours * 60
				break
			}
			prompt.Info("Choose an interval that divides 24 evenly, such as 1, 2, 3, 4, 6, 8, or 12 hours.")
		}
	}
	logonModes := []string{
		config.WindowsLogonBackground,
		config.WindowsLogonInteractive,
		config.WindowsLogonPassword,
	}
	logonIndex := 0
	for index, mode := range logonModes {
		if mode == previousLogonMode {
			logonIndex = index
		}
	}
	logonIndex, err = prompt.Select("When should scheduled backups run?", []Choice{
		{
			Label:       "Always, in the background",
			Description: "recommended; signed in or not, after reboots, no password stored",
		},
		{
			Label:       "Only while I'm signed in",
			Description: "no admin approval needed; stops when you sign out",
		},
		{
			Label:       "Always, with my Windows password",
			Description: "advanced; needed to save to a network share",
		},
	}, logonIndex)
	if err != nil {
		return err
	}
	profile.Schedule.Windows.LogonMode = logonModes[logonIndex]
	if err := profile.Schedule.Windows.Validate(); err != nil {
		return err
	}
	persistent, err := prompt.Confirm(
		"Catch up on a missed backup (for example after the server was off)?",
		true,
	)
	if err != nil {
		return err
	}
	profile.Schedule.Persistent = persistent
	if err := scheduler.Validate(ctx, profileName, *profile); err != nil {
		return err
	}
	switch profile.Schedule.Windows.LogonMode {
	case config.WindowsLogonBackground:
		prompt.Note("Backups run as your Windows account through Task Scheduler, even when nobody is signed in.")
		prompt.Note("Windows asks for administrator approval once when the task is created. No password is stored.")
	case config.WindowsLogonPassword:
		prompt.Note("You will be asked for your Windows password after review; Task Scheduler stores it using Windows.")
		prompt.Note("The account needs the Log on as a batch job right. No password is placed in TOML, task XML, arguments, or logs.")
	default:
		prompt.Note("Backups only run while you are signed in to Windows on this machine.")
	}
	prompt.Note("Times use this machine's timezone; daylight-saving changes are handled by Windows.")
	if persistent {
		prompt.Note("A missed backup runs once when the machine is available again, usually within about 10 minutes.")
	}
	return nil
}

func printSummary(prompt *Prompter, name string, profile config.Profile, paths config.Paths, notifications string) {
	connection := describeEndpoint(profile.Database)
	if profile.Database.Socket != "" {
		connection = "socket " + connection
	}
	schedule := "manual only"
	switch profile.Schedule.Mode {
	case config.ScheduleModeSystemd:
		schedule = profile.Schedule.OnCalendar + " " + profile.Schedule.Timezone
	case config.ScheduleModeWindows:
		schedule = windowsScheduleSummary(profile.Schedule.Windows)
	}
	rows := [][2]string{
		{"Profile", name},
		{"Database", profile.Database.Database + " (" + profile.Database.Flavor + ")"},
		{"Connection", connection + " as " + profile.Database.Username},
		{"Backup folder", profile.Backup.Directory},
		{"Compression", fmt.Sprintf("Zstandard level %d", profile.Backup.CompressionLevel)},
		{"Retention", retentionSummary(profile.Retention)},
		{"Schedule", schedule},
	}
	if profile.Schedule.Mode == config.ScheduleModeWindows {
		rows = append(rows, [2]string{"Runs", windowsLogonSummary(profile.Schedule.Windows.EffectiveLogonMode())})
	}
	if notifications != "" {
		rows = append(rows, [2]string{"Notifications", notifications})
	}
	rows = append(rows,
		[2]string{"Configuration", paths.Config},
		[2]string{"Credentials", paths.Credentials + " (password is not shown)"},
	)
	prompt.Summary(rows)
}

func retentionSummary(retention config.Retention) string {
	switch {
	case retention.KeepDays > 0 && retention.KeepLast > 0:
		return fmt.Sprintf("%d days, and always the newest %d", retention.KeepDays, retention.KeepLast)
	case retention.KeepDays > 0:
		return fmt.Sprintf("%d days", retention.KeepDays)
	case retention.KeepLast > 0:
		return fmt.Sprintf("newest %d backups", retention.KeepLast)
	default:
		return "keep everything"
	}
}

func windowsLogonSummary(mode string) string {
	switch mode {
	case config.WindowsLogonBackground:
		return "in the background, signed in or not"
	case config.WindowsLogonPassword:
		return "unattended with Windows-managed stored credential"
	default:
		return "only while you are signed in"
	}
}

func windowsScheduleSummary(schedule config.WindowsSchedule) string {
	if schedule.Frequency == config.WindowsFrequencyDaily {
		return "daily at " + schedule.StartTime + " local time"
	}
	every := fmt.Sprintf("%d minutes", schedule.IntervalMinutes)
	if schedule.IntervalMinutes%60 == 0 {
		every = fmt.Sprintf("%d hours", schedule.IntervalMinutes/60)
	}
	return fmt.Sprintf("every %s from %s local time", every, schedule.StartTime)
}

func loadExisting(path string) (config.Config, bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return config.Config{}, false, nil
	} else if err != nil {
		return config.Config{}, false, fmt.Errorf("inspect existing configuration: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, true, err
	}
	return cfg, true, nil
}

func validateNewProfileName(existing map[string]config.Profile) func(string) error {
	return func(value string) error {
		if !profileNamePattern.MatchString(value) {
			return errors.New("use letters, numbers, dots, dashes, or underscores")
		}
		if _, ok := existing[value]; ok {
			return errors.New("that profile already exists")
		}
		return nil
	}
}

func validateWritableDirectory(path string) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(path, ".fivemesh-backup-write-test-*")
	if err != nil {
		return err
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return nil
}

func requireNonempty(label string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
}

func actionableConnectionError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "access denied"):
		return "The database rejected the username or password. Check the credentials and grants."
	case strings.Contains(message, "connection refused"):
		return "No database server accepted the connection. Check the host, port, and server status."
	case strings.Contains(message, "unknown host"), strings.Contains(message, "lookup"):
		return "The database host could not be resolved. Check the host name."
	case strings.Contains(message, "can't connect"), strings.Contains(message, "cannot connect"):
		return "The database client could not connect. Check the socket path or network address."
	default:
		return "Connection test failed: " + err.Error()
	}
}

func detectSocket() string {
	candidates := []string{
		"/run/mysqld/mysqld.sock",
		"/var/run/mysqld/mysqld.sock",
		"/run/mysql/mysql.sock",
		"/var/lib/mysql/mysql.sock",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/run/mysqld/mysqld.sock"
}

func localTimezone() string {
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		value := strings.TrimSpace(string(data))
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	if value := os.Getenv("TZ"); value != "" {
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	value := time.Local.String()
	if value != "" && value != "Local" {
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	return "UTC"
}

func validateClock(value string) error {
	if _, _, err := parseClock(value); err != nil {
		return err
	}
	return nil
}

func calendarForInterval(hours int, clock string) string {
	hour, minute, _ := parseClock(clock)
	values := make([]int, 0, 24/hours)
	for offset := 0; offset < 24; offset += hours {
		values = append(values, (hour+offset)%24)
	}
	sort.Ints(values)
	hourText := make([]string, len(values))
	for index, value := range values {
		hourText[index] = fmt.Sprintf("%02d", value)
	}
	return fmt.Sprintf("*-*-* %s:%02d:00", strings.Join(hourText, ","), minute)
}

func parseClock(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("use HH:MM, for example 06:00")
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, errors.New("use a valid 24-hour time, for example 06:00")
	}
	return hour, minute, nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	divisor, exponent := int64(unit), 0
	for value := bytes / unit; value >= unit && exponent < 5; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGTPE"[exponent])
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func defaultInt(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func defaultInt64(value, fallback int64) int64 {
	if value != 0 {
		return value
	}
	return fallback
}

func cloneConfig(cfg config.Config) config.Config {
	clone := config.Config{
		Version:        cfg.Version,
		DefaultProfile: cfg.DefaultProfile,
		Profiles:       make(map[string]config.Profile, len(cfg.Profiles)),
		Notifications:  cfg.Notifications,
	}
	clone.Notifications.Discord.Events = append([]string(nil), cfg.Notifications.Discord.Events...)
	for name, profile := range cfg.Profiles {
		profile.Database.ExtraDumpArgs = append([]string(nil), profile.Database.ExtraDumpArgs...)
		clone.Profiles[name] = profile
	}
	return clone
}

func cloneCredentials(credentials config.CredentialFile) config.CredentialFile {
	clone := config.CredentialFile{
		Profiles:      make(map[string]config.Credential, len(credentials.Profiles)),
		Notifications: credentials.Notifications,
	}
	for name, credential := range credentials.Profiles {
		clone.Profiles[name] = credential
	}
	return clone
}

func rollbackConfiguration(
	paths config.Paths,
	existed bool,
	cfg config.Config,
	credentials config.CredentialFile,
	credentialsExisted bool,
) error {
	if !existed {
		var failures []string
		for _, path := range []string{paths.Config, paths.Credentials} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			}
		}
		if len(failures) > 0 {
			return errors.New(strings.Join(failures, "; "))
		}
		return nil
	}
	if credentialsExisted {
		return config.SaveReconfigured(cfg, credentials, paths)
	}
	if err := config.SaveConfig(cfg, paths.Config); err != nil {
		return err
	}
	if err := os.Remove(paths.Credentials); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func schedulingRollbackError(scheduleErr, configRollbackErr, scheduleRollbackErr error) error {
	if configRollbackErr == nil && scheduleRollbackErr == nil {
		return fmt.Errorf("scheduling failed; previous configuration and timer state were restored: %w", scheduleErr)
	}
	return fmt.Errorf(
		"scheduling failed and automatic rollback was incomplete: schedule=%w configuration_rollback=%v timer_rollback=%v; run \"fivemesh-backup schedule status\" and \"fivemesh-backup schedule remove\" before retrying",
		scheduleErr,
		configRollbackErr,
		scheduleRollbackErr,
	)
}

func restoreScheduleState(
	parent context.Context,
	scheduler ScheduleManager,
	profileName string,
	snapshot []byte,
	secret schedule.LogonSecret,
) error {
	recoveryCtx, cancel := recovery.Context(parent)
	defer cancel()
	return scheduler.Restore(recoveryCtx, profileName, snapshot, secret)
}

// defaultListDatabases is replaced in tests so that they never reach a real
// database server installed on the developer machine.
var defaultListDatabases = mysql.ListDatabases
