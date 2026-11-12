package wizard

import (
	"context"
	"errors"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
)

// SendFunc delivers one notification to a webhook.
type SendFunc func(ctx context.Context, webhookURL string, settings config.DiscordNotifications, event notify.Event) error

func defaultSend(ctx context.Context, webhookURL string, settings config.DiscordNotifications, event notify.Event) error {
	ctx, cancel := context.WithTimeout(ctx, notify.Timeout)
	defer cancel()
	return notify.NewDiscord(webhookURL, settings).Send(ctx, event)
}

// eventPresets are the choices offered for which events to send.
var eventPresets = []struct {
	choice Choice
	events []string
}{
	{Choice{Label: "Everything", Description: "backups, failures, restores, and setting changes"}, nil},
	{Choice{Label: "Backups and failures", Description: "skip restores and setting changes"}, []string{config.NotifySuccess, config.NotifyFailure}},
	{Choice{Label: "Only problems", Description: "failed backups and restores"}, []string{config.NotifyFailure}},
}

// DiscordSetup is the result of the Discord questions.
type DiscordSetup struct {
	Settings   config.DiscordNotifications
	WebhookURL string
}

// ConfigureDiscord asks for a webhook and preferences, then sends a test
// message so a wrong URL is caught immediately. It returns ErrSkipped when
// the user decides not to set it up after all.
func ConfigureDiscord(ctx context.Context, prompt *Prompter, current config.DiscordNotifications, currentWebhook string, send SendFunc) (DiscordSetup, error) {
	if send == nil {
		send = defaultSend
	}
	prompt.Note("In Discord: Server Settings → Integrations → Webhooks → New Webhook → Copy Webhook URL.")
	settings := current
	settings.Enabled = true
	for {
		label := "Discord webhook URL"
		if currentWebhook != "" {
			label += " (press Enter to keep the current one)"
		}
		webhook, err := prompt.Secret(label)
		if err != nil {
			return DiscordSetup{}, err
		}
		webhook = strings.TrimSpace(webhook)
		if webhook == "" {
			webhook = currentWebhook
		}
		if webhook == "" {
			skip, err := prompt.Confirm("Skip Discord notifications for now?", true)
			if err != nil {
				return DiscordSetup{}, err
			}
			if skip {
				return DiscordSetup{}, ErrSkipped
			}
			continue
		}
		normalized, duplicated, err := notify.NormalizeWebhookURL(webhook)
		if err != nil {
			prompt.Fail(err.Error())
			continue
		}
		webhook = normalized
		if duplicated {
			prompt.Note("The URL was pasted more than once; using it once.")
		}
		prompt.Success("Webhook " + notify.MaskWebhookURL(webhook))

		presetIndex := 0
		for index, preset := range eventPresets {
			if sameEvents(preset.events, settings.Events) {
				presetIndex = index
			}
		}
		presetIndex, err = prompt.Select("Which events should be posted?", choicesOf(), presetIndex)
		if err != nil {
			return DiscordSetup{}, err
		}
		settings.Events = append([]string(nil), eventPresets[presetIndex].events...)

		serverName := settings.ServerName
		if serverName == "" {
			serverName = notify.DefaultServerName()
		}
		settings.ServerName, err = prompt.Text("Name for this server in messages", serverName, func(value string) error {
			return config.DiscordNotifications{ServerName: value}.Validate()
		})
		if err != nil {
			return DiscordSetup{}, err
		}

		var sendErr error
		if taskErr := prompt.Task(ctx, "Sending a test message to Discord...", func(runContext context.Context) error {
			sendErr = send(runContext, webhook, settings, notify.TestEvent(settings))
			return nil
		}); taskErr != nil {
			return DiscordSetup{}, taskErr
		}
		if sendErr != nil {
			prompt.Fail("Discord did not accept the test message: " + sendErr.Error())
			retry, err := prompt.Confirm("Try another webhook URL?", true)
			if err != nil {
				return DiscordSetup{}, err
			}
			if retry {
				currentWebhook = ""
				continue
			}
			return DiscordSetup{}, ErrSkipped
		}
		prompt.Success("Test message sent. Check your Discord channel.")
		return DiscordSetup{Settings: settings, WebhookURL: webhook}, nil
	}
}

// ErrSkipped means the user chose not to configure notifications.
var ErrSkipped = errors.New("notifications skipped")

func choicesOf() []Choice {
	choices := make([]Choice, len(eventPresets))
	for index, preset := range eventPresets {
		choices[index] = preset.choice
	}
	return choices
}

func sameEvents(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, event := range left {
		seen[event] = true
	}
	for _, event := range right {
		if !seen[event] {
			return false
		}
	}
	return true
}

// NotificationSummary describes saved settings for review screens.
func NotificationSummary(settings config.DiscordNotifications) string {
	if !settings.Enabled {
		return "off"
	}
	for _, preset := range eventPresets {
		if sameEvents(preset.events, settings.Events) {
			return "Discord: " + strings.ToLower(preset.choice.Label)
		}
	}
	return "Discord: " + strings.Join(settings.Events, ", ")
}

// notifySaved reports a saved change. Delivery problems are shown as a
// warning; the configuration is already saved at this point.
func notifySaved(ctx context.Context, prompt *Prompter, send SendFunc, cfg config.Config, credentials config.CredentialFile, event notify.Event) {
	settings := cfg.Notifications.Discord
	webhook := credentials.Notifications.DiscordWebhookURL
	if webhook == "" || !settings.Wants(event.Kind.Category()) {
		return
	}
	if send == nil {
		send = defaultSend
	}
	if err := send(ctx, webhook, settings, event); err != nil {
		prompt.Warn("Discord notification was not delivered: " + err.Error())
	}
}
