// Package notify sends best-effort status notifications (currently to
// Discord webhooks). A notification failure never changes the outcome of a
// backup, restore, or configuration change; callers report it as a warning.
package notify

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/version"
)

// Kind identifies what happened.
type Kind string

const (
	BackupSucceeded  Kind = "backup-succeeded"
	BackupFailed     Kind = "backup-failed"
	RestoreSucceeded Kind = "restore-succeeded"
	RestoreFailed    Kind = "restore-failed"
	ConfigChanged    Kind = "config-changed"
	Test             Kind = "test"
)

// Category maps an event to the subscription category users choose from.
func (k Kind) Category() string {
	switch k {
	case BackupSucceeded:
		return config.NotifySuccess
	case BackupFailed:
		return config.NotifyFailure
	case RestoreSucceeded, RestoreFailed:
		return config.NotifyRestore
	case ConfigChanged:
		return config.NotifyConfig
	default:
		return ""
	}
}

// Failure reports whether the event describes something that went wrong.
func (k Kind) Failure() bool {
	return k == BackupFailed || k == RestoreFailed
}

// Field is a labelled detail shown with the event.
type Field struct {
	Name   string
	Value  string
	Inline bool
}

// Event is a single notification.
type Event struct {
	Kind    Kind
	Profile string
	// Title is a short headline, e.g. "Backup succeeded".
	Title string
	// Summary is an optional one-line description.
	Summary string
	Fields  []Field
	// Error is a sanitized error message for failures.
	Error string
	Time  time.Time
}

// Sender delivers an event.
type Sender interface {
	Send(context.Context, Event) error
}

// Timeout bounds every delivery, including retries, so a slow or unreachable
// webhook never holds up a scheduled backup.
const Timeout = 15 * time.Second

// ErrNotConfigured is returned when no destination is enabled.
var ErrNotConfigured = errors.New("Discord notifications are not configured; run \"fivemesh-backup notify discord\"")

// Notifier routes events to the configured destinations.
type Notifier struct {
	Settings config.DiscordNotifications
	Discord  Sender
}

// FromConfig builds a notifier from saved settings. It returns nil when
// notifications are disabled or incomplete, so callers can skip quietly.
func FromConfig(cfg config.Config, credentials config.CredentialFile) *Notifier {
	settings := cfg.Notifications.Discord
	webhook := credentials.Notifications.DiscordWebhookURL
	if !settings.Enabled || webhook == "" {
		return nil
	}
	return &Notifier{Settings: settings, Discord: NewDiscord(webhook, settings)}
}

// Load reads notification settings for commands that only have file paths.
// Missing or unreadable files mean "no notifications", never an error that
// could block the calling command.
func Load(paths config.Paths) *Notifier {
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil
	}
	credentials, err := config.LoadCredentials(paths.Credentials)
	if err != nil {
		return nil
	}
	return FromConfig(cfg, credentials)
}

// Notify sends the event if its category is subscribed. A nil notifier is a
// no-op. The delivery uses its own deadline so that it still runs after the
// caller's context was cancelled (for example a timed-out backup).
func (n *Notifier) Notify(event Event) error {
	if n == nil || n.Discord == nil {
		return nil
	}
	if event.Kind != Test && !n.Settings.Wants(event.Kind.Category()) {
		return nil
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	return n.Discord.Send(ctx, event)
}

// DefaultServerName is the label used when none is configured.
func DefaultServerName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "this server"
	}
	return name
}

func footerText(serverName string) string {
	if serverName == "" {
		serverName = DefaultServerName()
	}
	return "FiveMesh Backup " + version.Version + " · " + serverName
}
