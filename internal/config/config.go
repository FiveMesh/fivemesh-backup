package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/FiveMesh/fivemesh-backup/internal/winsec"
)

const CurrentVersion = 1

const (
	ScheduleModeManual  = "manual"
	ScheduleModeSystemd = "systemd"
	ScheduleModeWindows = "windows"

	WindowsFrequencyDaily    = "daily"
	WindowsFrequencyInterval = "interval"

	// WindowsLogonBackground runs the task as the current account whether or
	// not it is signed in, without storing a Windows password (Task Scheduler
	// S4U logon). Registration requires one administrator approval.
	WindowsLogonBackground  = "background"
	WindowsLogonInteractive = "interactive"
	WindowsLogonPassword    = "password"
)

// ValidWindowsLogonMode reports whether mode is a supported Task Scheduler
// logon mode name.
func ValidWindowsLogonMode(mode string) bool {
	switch mode {
	case WindowsLogonBackground, WindowsLogonInteractive, WindowsLogonPassword:
		return true
	}
	return false
}

var replaceFileAtomic = replaceAtomic

type Config struct {
	Version        int                `toml:"version"`
	DefaultProfile string             `toml:"default_profile"`
	Profiles       map[string]Profile `toml:"profiles"`
	Notifications  Notifications      `toml:"notifications,omitempty"`
}

// Notification event categories a user can subscribe to.
const (
	NotifySuccess = "success"
	NotifyFailure = "failure"
	NotifyRestore = "restore"
	NotifyConfig  = "config"
)

// AllNotifyEvents lists every category in display order.
var AllNotifyEvents = []string{NotifySuccess, NotifyFailure, NotifyRestore, NotifyConfig}

// Notifications holds non-secret notification settings. Webhook URLs are
// secrets and live in the credentials file.
type Notifications struct {
	Discord DiscordNotifications `toml:"discord,omitempty"`
}

type DiscordNotifications struct {
	Enabled bool `toml:"enabled,omitempty"`
	// Events are the categories to send; empty means all of them.
	Events []string `toml:"events,omitempty"`
	// ServerName labels messages so several machines can share a channel.
	ServerName string `toml:"server_name,omitempty"`
	// MentionOnFailure is a Discord mention added to failure messages, for
	// example "<@&123456789012345678>" for a role or "<@123...>" for a user.
	MentionOnFailure string `toml:"mention_on_failure,omitempty"`
}

// Wants reports whether the event category should be sent.
func (d DiscordNotifications) Wants(category string) bool {
	if !d.Enabled {
		return false
	}
	if len(d.Events) == 0 {
		return true
	}
	for _, event := range d.Events {
		if event == category {
			return true
		}
	}
	return false
}

var discordMention = regexp.MustCompile(`^<@&?[0-9]{15,22}>$`)

func (d DiscordNotifications) Validate() error {
	for _, event := range d.Events {
		valid := false
		for _, known := range AllNotifyEvents {
			if event == known {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("notifications.discord.events: unknown event %q (use %s)", event, strings.Join(AllNotifyEvents, ", "))
		}
	}
	if len(d.ServerName) > 80 || containsControl(d.ServerName) {
		return errors.New("notifications.discord.server_name must be at most 80 printable characters")
	}
	if d.MentionOnFailure != "" && !discordMention.MatchString(d.MentionOnFailure) {
		return errors.New(`notifications.discord.mention_on_failure must look like <@&ROLE_ID> or <@USER_ID>`)
	}
	return nil
}

type Profile struct {
	Description string    `toml:"description,omitempty"`
	Database    Database  `toml:"database"`
	Backup      Backup    `toml:"backup"`
	Retention   Retention `toml:"retention"`
	Schedule    Schedule  `toml:"schedule"`
}

type Database struct {
	Engine            string   `toml:"engine"`
	Flavor            string   `toml:"flavor,omitempty"`
	DumpTool          string   `toml:"dump_tool"`
	ClientTool        string   `toml:"client_tool"`
	Host              string   `toml:"host,omitempty"`
	Port              int      `toml:"port,omitempty"`
	Socket            string   `toml:"socket,omitempty"`
	Username          string   `toml:"username"`
	Database          string   `toml:"database"`
	SingleTransaction bool     `toml:"single_transaction"`
	IncludeRoutines   bool     `toml:"include_routines"`
	IncludeEvents     bool     `toml:"include_events"`
	ExtraDumpArgs     []string `toml:"extra_dump_args,omitempty"`
}

type Backup struct {
	Directory             string `toml:"directory"`
	CompressionLevel      int    `toml:"compression_level"`
	Timeout               string `toml:"timeout,omitempty"`
	MinimumFreeSpaceBytes int64  `toml:"minimum_free_space_bytes"`
}

type Retention struct {
	KeepLast     int   `toml:"keep_last"`
	KeepDays     int   `toml:"keep_days"`
	MaximumBytes int64 `toml:"maximum_bytes"`
}

type Schedule struct {
	Mode       string          `toml:"mode,omitempty"`
	OnCalendar string          `toml:"on_calendar,omitempty"`
	Timezone   string          `toml:"timezone,omitempty"`
	Persistent bool            `toml:"persistent,omitempty"`
	Paused     bool            `toml:"paused,omitempty"`
	Windows    WindowsSchedule `toml:"windows,omitempty"`
}

type WindowsSchedule struct {
	Frequency       string `toml:"frequency,omitempty"`
	IntervalMinutes int    `toml:"interval_minutes,omitempty"`
	StartTime       string `toml:"start_time,omitempty"`
	LogonMode       string `toml:"logon_mode,omitempty"`
}

type Paths struct {
	Config      string
	Credentials string
	StateDir    string
}

type CredentialFile struct {
	Profiles      map[string]Credential  `toml:"profiles"`
	Notifications NotificationCredential `toml:"notifications,omitempty"`
}

// NotificationCredential stores notification secrets. A Discord webhook URL
// lets anyone post to the channel, so it is protected like a password.
type NotificationCredential struct {
	DiscordWebhookURL string `toml:"discord_webhook_url,omitempty"`
}

type Credential struct {
	Password string `toml:"password"`
}

type ResolvedProfile struct {
	Name        string
	Profile     Profile
	Password    string
	Credentials string
}

func DefaultPaths() (Paths, error) {
	if home := os.Getenv("FIVEMESH_BACKUP_HOME"); home != "" {
		return Paths{
			Config:      filepath.Join(home, "config.toml"),
			Credentials: filepath.Join(home, "credentials.toml"),
			StateDir:    filepath.Join(home, "state"),
		}, nil
	}

	configHome, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	root := filepath.Join(configHome, "fivemesh-backup")
	return Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}, nil
}

func Load(path string) (Config, error) {
	if err := winsec.ProtectDirectory(filepath.Dir(path)); err != nil {
		return Config{}, fmt.Errorf("protect configuration directory: %w", err)
	}
	if err := winsec.ProtectFile(path); err != nil {
		return Config{}, fmt.Errorf("protect configuration file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration %q: %w", path, err)
	}

	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration %q: %w", path, err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported version %d; expected %d", c.Version, CurrentVersion)
	}
	if len(c.Profiles) == 0 {
		return errors.New("at least one backup profile is required")
	}
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("default profile %q does not exist", c.DefaultProfile)
		}
	}

	for name, profile := range c.Profiles {
		if err := validateProfileName(name); err != nil {
			return err
		}
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return c.Notifications.Discord.Validate()
}

func (p Profile) Validate() error {
	if p.Database.Engine != "mysql" {
		return fmt.Errorf("database.engine must be %q", "mysql")
	}
	if p.Database.Flavor != "" && p.Database.Flavor != "mariadb" && p.Database.Flavor != "mysql" {
		return errors.New("database.flavor must be mariadb or mysql")
	}
	if strings.TrimSpace(p.Database.DumpTool) == "" {
		return errors.New("database.dump_tool is required")
	}
	if strings.TrimSpace(p.Database.ClientTool) == "" {
		return errors.New("database.client_tool is required")
	}
	if strings.TrimSpace(p.Database.Username) == "" {
		return errors.New("database.username is required")
	}
	if err := validateDatabaseName(p.Database.Database); err != nil {
		return fmt.Errorf("database.database: %w", err)
	}
	if p.Database.Host == "" && p.Database.Socket == "" {
		return errors.New("one of database.host or database.socket is required")
	}
	if p.Database.Host != "" && p.Database.Socket != "" {
		return errors.New("database.host and database.socket are mutually exclusive")
	}
	if p.Database.Host != "" && (p.Database.Port < 1 || p.Database.Port > 65535) {
		return errors.New("database.port must be between 1 and 65535 when database.host is set")
	}
	if p.Database.Host != "" {
		for _, value := range []string{p.Database.Host, p.Database.Username} {
			if containsControl(value) {
				return errors.New("database host and username must not contain control characters")
			}
		}
	} else if containsControl(p.Database.Username) {
		return errors.New("database username must not contain control characters")
	}
	if p.Database.Socket != "" && containsControl(p.Database.Socket) {
		return errors.New("database.socket must not contain control characters")
	}
	for _, arg := range p.Database.ExtraDumpArgs {
		if arg == "" || containsControl(arg) {
			return errors.New("database.extra_dump_args must contain non-empty printable values")
		}
		lower := strings.ToLower(strings.TrimSpace(arg))
		if strings.HasPrefix(lower, "--password") ||
			strings.HasPrefix(lower, "--login-path") ||
			strings.HasPrefix(lower, "--defaults-file") ||
			strings.HasPrefix(lower, "--defaults-extra-file") ||
			strings.HasPrefix(lower, "-p") {
			return fmt.Errorf("database.extra_dump_args must not override credentials with %q", arg)
		}
	}
	if strings.TrimSpace(p.Backup.Directory) == "" {
		return errors.New("backup.directory is required")
	}
	if p.Backup.CompressionLevel < 1 || p.Backup.CompressionLevel > 19 {
		return errors.New("backup.compression_level must be between 1 and 19")
	}
	if p.Backup.MinimumFreeSpaceBytes < 0 {
		return errors.New("backup.minimum_free_space_bytes must not be negative")
	}
	if _, err := p.Backup.TimeoutDuration(); err != nil {
		return err
	}
	if p.Retention.KeepLast < 0 || p.Retention.KeepDays < 0 || p.Retention.MaximumBytes < 0 {
		return errors.New("retention values must not be negative")
	}
	if p.Schedule.Mode != "" &&
		p.Schedule.Mode != ScheduleModeManual &&
		p.Schedule.Mode != ScheduleModeSystemd &&
		p.Schedule.Mode != ScheduleModeWindows {
		return errors.New("schedule.mode must be manual, systemd, or windows")
	}
	if p.Schedule.Mode == ScheduleModeSystemd {
		if strings.TrimSpace(p.Schedule.OnCalendar) == "" {
			return errors.New("schedule.on_calendar is required for systemd mode")
		}
		if containsControl(p.Schedule.OnCalendar) || len(p.Schedule.OnCalendar) > 256 {
			return errors.New("schedule.on_calendar must be printable and at most 256 bytes")
		}
	}
	if p.Schedule.Mode == ScheduleModeWindows {
		if err := p.Schedule.Windows.Validate(); err != nil {
			return fmt.Errorf("schedule.windows: %w", err)
		}
		if strings.TrimSpace(p.Schedule.OnCalendar) != "" {
			return errors.New("schedule.on_calendar is not valid for Windows Task Scheduler mode")
		}
		if p.Schedule.Timezone != "" && p.Schedule.Timezone != "Local" {
			return errors.New("schedule.timezone must be Local or empty for Windows Task Scheduler mode")
		}
	}
	if p.Schedule.Timezone != "" {
		if containsControl(p.Schedule.Timezone) {
			return errors.New("schedule.timezone must not contain control characters")
		}
		if _, err := time.LoadLocation(p.Schedule.Timezone); err != nil {
			return fmt.Errorf("schedule.timezone is invalid: %w", err)
		}
	}
	return nil
}

func (s Schedule) IsAutomatic() bool {
	return s.Mode == ScheduleModeSystemd || s.Mode == ScheduleModeWindows
}

func (s WindowsSchedule) Validate() error {
	switch s.Frequency {
	case WindowsFrequencyDaily:
		if s.IntervalMinutes != 0 {
			return errors.New("interval_minutes must be zero for daily frequency")
		}
	case WindowsFrequencyInterval:
		if s.IntervalMinutes < 15 || s.IntervalMinutes > 24*60 {
			return errors.New("interval_minutes must be between 15 and 1440")
		}
		if (24*60)%s.IntervalMinutes != 0 {
			return errors.New("interval_minutes must divide evenly into 24 hours")
		}
	default:
		return errors.New("frequency must be daily or interval")
	}
	if _, _, err := ParseClock(s.StartTime); err != nil {
		return fmt.Errorf("start_time: %w", err)
	}
	if s.LogonMode != "" && !ValidWindowsLogonMode(s.LogonMode) {
		return errors.New("logon_mode must be background, interactive, or password")
	}
	return nil
}

func (s WindowsSchedule) EffectiveLogonMode() string {
	if s.LogonMode == "" {
		return WindowsLogonInteractive
	}
	return s.LogonMode
}

func ParseClock(value string) (hour int, minute int, err error) {
	if strings.TrimSpace(value) != value || len(value) != 5 || value[2] != ':' {
		return 0, 0, errors.New("use HH:MM, for example 06:00")
	}
	hour, hourErr := strconv.Atoi(value[:2])
	minute, minuteErr := strconv.Atoi(value[3:])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, errors.New("use a valid 24-hour time, for example 06:00")
	}
	return hour, minute, nil
}

func (p Profile) BackupTimeout(defaultValue time.Duration) time.Duration {
	timeout, err := p.Backup.TimeoutDuration()
	if err != nil {
		return defaultValue
	}
	return timeout
}

func (b Backup) TimeoutDuration() (time.Duration, error) {
	if strings.TrimSpace(b.Timeout) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(b.Timeout)
	if err != nil {
		return 0, fmt.Errorf("backup.timeout is invalid: %w", err)
	}
	if duration <= 0 {
		return 0, errors.New("backup.timeout must be greater than zero")
	}
	return duration, nil
}

func (c Config) ResolveProfile(name string) (string, Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" && len(c.Profiles) == 1 {
		for onlyName := range c.Profiles {
			name = onlyName
		}
	}
	if name == "" {
		return "", Profile{}, errors.New("profile is required when no default profile is configured")
	}
	profile, ok := c.Profiles[name]
	if !ok {
		return "", Profile{}, fmt.Errorf("profile %q does not exist", name)
	}
	return name, profile, nil
}

func LoadCredential(path, profile string) (string, error) {
	credentials, err := LoadCredentials(path)
	if err != nil {
		return "", err
	}
	credential, ok := credentials.Profiles[profile]
	if !ok {
		return "", fmt.Errorf("credentials do not contain profile %q", profile)
	}
	if containsControl(credential.Password) || strings.ContainsAny(credential.Password, "\r\n\x00") {
		return "", fmt.Errorf("credential for profile %q contains unsupported control characters", profile)
	}
	return credential.Password, nil
}

func LoadCredentials(path string) (CredentialFile, error) {
	if err := winsec.ProtectDirectory(filepath.Dir(path)); err != nil {
		return CredentialFile{}, fmt.Errorf("protect credentials directory: %w", err)
	}
	if err := winsec.ProtectFile(path); err != nil {
		return CredentialFile{}, fmt.Errorf("protect credentials file: %w", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			return CredentialFile{}, fmt.Errorf("read credentials %q: %w", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return CredentialFile{}, fmt.Errorf("credentials %q must not be readable by group or others (found mode %04o)", path, info.Mode().Perm())
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return CredentialFile{}, fmt.Errorf("read credentials %q: %w", path, err)
	}
	var credentials CredentialFile
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return CredentialFile{}, fmt.Errorf("decode credentials %q: %w", path, err)
	}
	return credentials, nil
}

func SaveInitial(cfg Config, credentials CredentialFile, paths Paths) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Credentials), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := protectConfigurationPaths(paths); err != nil {
		return err
	}
	if _, err := os.Stat(paths.Config); err == nil {
		return fmt.Errorf("configuration %q already exists", paths.Config)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect configuration path: %w", err)
	}
	if _, err := os.Stat(paths.Credentials); err == nil {
		return fmt.Errorf("credentials %q already exist", paths.Credentials)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect credentials path: %w", err)
	}
	return SaveReconfigured(cfg, credentials, paths)
}

// SaveReconfigured replaces configuration and credentials while retaining
// snapshots of both prior files. Validation happens before either file is
// changed. If the second replacement fails, the first is rolled back.
func SaveReconfigured(cfg Config, credentials CredentialFile, paths Paths) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Credentials), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := protectConfigurationPaths(paths); err != nil {
		return err
	}
	for _, path := range []string{paths.Config, paths.Credentials} {
		if err := protectExistingSensitiveFile(path); err != nil {
			return err
		}
	}
	configSnapshot, err := snapshotFile(paths.Config)
	if err != nil {
		return err
	}
	credentialSnapshot, err := snapshotFile(paths.Credentials)
	if err != nil {
		return err
	}
	configData, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	credentialData, err := toml.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	configTemp, err := writeTemporary(paths.Config, configData, 0o600)
	if err != nil {
		return fmt.Errorf("prepare configuration: %w", err)
	}
	defer os.Remove(configTemp)
	credentialTemp, err := writeTemporary(paths.Credentials, credentialData, 0o600)
	if err != nil {
		return fmt.Errorf("prepare credentials: %w", err)
	}
	defer os.Remove(credentialTemp)

	if err := replaceFileAtomic(credentialTemp, paths.Credentials); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}
	if err := winsec.VerifyFile(paths.Credentials); err != nil {
		credentialRollbackErr := restoreSnapshot(paths.Credentials, credentialSnapshot)
		if credentialRollbackErr != nil {
			return fmt.Errorf("verify credentials ACL: %w; restore prior credentials: %v", err, credentialRollbackErr)
		}
		return fmt.Errorf("verify credentials ACL: %w", err)
	}
	if err := replaceFileAtomic(configTemp, paths.Config); err != nil {
		credentialRollbackErr := restoreSnapshot(paths.Credentials, credentialSnapshot)
		configRollbackErr := restoreSnapshot(paths.Config, configSnapshot)
		if credentialRollbackErr != nil || configRollbackErr != nil {
			return fmt.Errorf(
				"replace configuration: %w; rollback errors: credentials=%v config=%v",
				err,
				credentialRollbackErr,
				configRollbackErr,
			)
		}
		return fmt.Errorf("replace configuration: %w", err)
	}
	if err := winsec.VerifyFile(paths.Config); err != nil {
		credentialRollbackErr := restoreSnapshot(paths.Credentials, credentialSnapshot)
		configRollbackErr := restoreSnapshot(paths.Config, configSnapshot)
		if credentialRollbackErr != nil || configRollbackErr != nil {
			return fmt.Errorf(
				"verify configuration ACL: %w; rollback errors: credentials=%v config=%v",
				err,
				credentialRollbackErr,
				configRollbackErr,
			)
		}
		return fmt.Errorf("verify configuration ACL: %w", err)
	}
	return nil
}

func SaveConfig(cfg Config, path string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	if err := winsec.ProtectDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("protect configuration directory: %w", err)
	}
	if err := protectExistingSensitiveFile(path); err != nil {
		return err
	}
	snapshot, err := snapshotFile(path)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	temp, err := writeTemporary(path, data, 0o600)
	if err != nil {
		return fmt.Errorf("prepare configuration: %w", err)
	}
	defer os.Remove(temp)
	if err := replaceFileAtomic(temp, path); err != nil {
		if rollbackErr := restoreSnapshot(path, snapshot); rollbackErr != nil {
			return fmt.Errorf("replace configuration: %w; restore prior configuration: %v", err, rollbackErr)
		}
		return fmt.Errorf("replace configuration: %w", err)
	}
	if err := winsec.VerifyFile(path); err != nil {
		if rollbackErr := restoreSnapshot(path, snapshot); rollbackErr != nil {
			return fmt.Errorf("verify configuration ACL: %w; restore prior configuration: %v", err, rollbackErr)
		}
		return fmt.Errorf("verify configuration ACL: %w", err)
	}
	return nil
}

func EnsureStateDirs(stateDir string) (locksDir, logsDir string, err error) {
	locksDir = filepath.Join(stateDir, "locks")
	logsDir = filepath.Join(stateDir, "logs")
	for _, dir := range []string{stateDir, locksDir, logsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", fmt.Errorf("create state directory %q: %w", dir, err)
		}
	}
	return locksDir, logsDir, nil
}

func writeTemporary(path string, data []byte, mode os.FileMode) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".partial-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	complete := false
	defer func() {
		_ = temp.Close()
		if !complete {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return "", err
	}
	if err := winsec.ProtectFile(tempName); err != nil {
		return "", fmt.Errorf("protect temporary sensitive file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	complete = true
	return tempName, nil
}

type fileSnapshot struct {
	exists bool
	data   []byte
	mode   os.FileMode
}

func snapshotFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read prior file %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("inspect prior file %q: %w", path, err)
	}
	return fileSnapshot{exists: true, data: data, mode: info.Mode().Perm()}, nil
}

func restoreSnapshot(path string, snapshot fileSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	temp, err := writeTemporary(path, snapshot.data, snapshot.mode)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := replaceFileAtomic(temp, path); err != nil {
		return err
	}
	if err := winsec.VerifyFile(path); err != nil {
		return fmt.Errorf("verify restored sensitive file ACL: %w", err)
	}
	return nil
}

func validateProfileName(name string) error {
	if name == "" {
		return errors.New("profile name must not be empty")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) || containsControl(name) {
		return fmt.Errorf("profile name %q is invalid", name)
	}
	return nil
}

func validateDatabaseName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > 64 {
		return errors.New("name must not exceed 64 bytes")
	}
	if strings.HasPrefix(name, "-") || containsControl(name) {
		return errors.New("name must not start with '-' or contain control characters")
	}
	return nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func protectConfigurationPaths(paths Paths) error {
	for _, directory := range []string{
		filepath.Dir(paths.Config),
		filepath.Dir(paths.Credentials),
		paths.StateDir,
	} {
		if err := winsec.ProtectDirectory(directory); err != nil {
			return fmt.Errorf("protect sensitive directory %q: %w", directory, err)
		}
	}
	return nil
}

func protectExistingSensitiveFile(path string) error {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sensitive file %q: %w", path, err)
	}
	if err := winsec.ProtectFile(path); err != nil {
		return fmt.Errorf("protect sensitive file %q: %w", path, err)
	}
	return nil
}
