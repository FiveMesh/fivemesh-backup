package wizard

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
)

const wizardTestWebhook = "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz_secret-token"

func TestWizardDiscordStepSavesWebhookAndReportsProfile(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	script := strings.Join([]string{
		"", "", "2", "", "", "", "secret", "fxserver", // profile and database
		filepath.Join(root, "backups"), "", "", "", // storage
		"1",               // manual schedule
		"y",               // Discord notifications
		"not-a-url",       // rejected
		wizardTestWebhook, // accepted
		"3",               // only problems
		"eu-1",            // server name
		"",                // save
		"n",               // no test backup
	}, "\n") + "\n"
	var sent []notify.Event
	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		Paths:         paths,
		Prompter:      NewPrompter(strings.NewReader(script), &output),
		DiscoverTools: fakeDiscover,
		TestConnection: func(context.Context, mysql.ToolInfo, config.Database, string) error {
			return nil
		},
		Scheduler:     &fakeScheduler{},
		Notifications: true,
		SendNotification: func(_ context.Context, webhook string, _ config.DiscordNotifications, event notify.Event) error {
			if webhook != wizardTestWebhook {
				t.Errorf("unexpected webhook %q", webhook)
			}
			sent = append(sent, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, output.String())
	}
	cfg, _ := config.Load(paths.Config)
	discord := cfg.Notifications.Discord
	if !discord.Enabled || discord.ServerName != "eu-1" || strings.Join(discord.Events, ",") != config.NotifyFailure {
		t.Fatalf("unexpected settings: %#v", discord)
	}
	credentials, _ := config.LoadCredentials(paths.Credentials)
	if credentials.Notifications.DiscordWebhookURL != wizardTestWebhook {
		t.Fatal("webhook was not saved")
	}
	// Only the test message: "only problems" does not include config changes.
	if len(sent) != 1 || sent[0].Kind != notify.Test {
		t.Fatalf("sent = %#v", sent)
	}
	if strings.Contains(output.String(), "secret-token") {
		t.Fatal("webhook token was echoed")
	}
	if !strings.Contains(output.String(), "Notifications: Discord: only problems") {
		t.Fatalf("review is missing the notification summary:\n%s", output.String())
	}
}

func TestConfigureDiscordFailedTestCanBeSkipped(t *testing.T) {
	var output bytes.Buffer
	prompt := NewPrompter(strings.NewReader(wizardTestWebhook+"\n\n\nn\n"), &output)
	_, err := ConfigureDiscord(context.Background(), prompt, config.DiscordNotifications{}, "",
		func(context.Context, string, config.DiscordNotifications, notify.Event) error {
			return errors.New("Discord rejected the webhook")
		})
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(output.String(), "Discord did not accept the test message") {
		t.Fatalf("output:\n%s", output.String())
	}
}

func TestConfigureDiscordAcceptsDoublePastedWebhook(t *testing.T) {
	var output bytes.Buffer
	var sentTo string
	prompt := NewPrompter(strings.NewReader(wizardTestWebhook+wizardTestWebhook+"\n\n\n"), &output)
	setup, err := ConfigureDiscord(context.Background(), prompt, config.DiscordNotifications{}, "",
		func(_ context.Context, webhook string, _ config.DiscordNotifications, _ notify.Event) error {
			sentTo = webhook
			return nil
		})
	if err != nil {
		t.Fatalf("double-pasted webhook was rejected: %v\n%s", err, output.String())
	}
	if setup.WebhookURL != wizardTestWebhook || sentTo != wizardTestWebhook {
		t.Fatalf("webhook = %q, test sent to %q", setup.WebhookURL, sentTo)
	}
	text := output.String()
	if !strings.Contains(text, "pasted more than once") || !strings.Contains(text, "/api/webhooks/123456789012345678/") {
		t.Fatalf("missing duplicate note or masked confirmation:\n%s", text)
	}
	if strings.Contains(text, "secret-token") {
		t.Fatal("webhook token was shown")
	}
}
