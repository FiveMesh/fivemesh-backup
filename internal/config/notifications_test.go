package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotificationSettingsRoundTrip(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := validConfig()
	cfg.Notifications.Discord = DiscordNotifications{
		Enabled:          true,
		Events:           []string{NotifyFailure, NotifyConfig},
		ServerName:       "eu-1",
		MentionOnFailure: "<@&123456789012345678>",
	}
	credentials := CredentialFile{
		Profiles:      map[string]Credential{"production": {Password: "secret"}},
		Notifications: NotificationCredential{DiscordWebhookURL: "https://discord.com/api/webhooks/123456789012345678/token"},
	}
	if err := SaveInitial(cfg, credentials, paths); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	discord := loaded.Notifications.Discord
	if !discord.Enabled || discord.ServerName != "eu-1" || len(discord.Events) != 2 || discord.MentionOnFailure == "" {
		t.Fatalf("notification settings did not round-trip: %#v", discord)
	}
	loadedCredentials, err := LoadCredentials(paths.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if loadedCredentials.Notifications.DiscordWebhookURL != credentials.Notifications.DiscordWebhookURL {
		t.Fatal("webhook URL did not round-trip")
	}
	data, _ := os.ReadFile(paths.Config)
	if strings.Contains(string(data), "webhooks") {
		t.Fatal("webhook URL leaked into config.toml")
	}
}

func TestEmptyNotificationsAreNotWritten(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	if err := SaveInitial(validConfig(), CredentialFile{Profiles: map[string]Credential{"production": {Password: "x"}}}, paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Config, paths.Credentials} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "notifications") {
			t.Fatalf("%s contains an empty notifications section:\n%s", filepath.Base(path), data)
		}
	}
}

func TestDiscordNotificationValidation(t *testing.T) {
	cases := map[string]DiscordNotifications{
		"unknown event": {Events: []string{"everything"}},
		"bad mention":   {MentionOnFailure: "@everyone"},
		"long name":     {ServerName: strings.Repeat("x", 81)},
	}
	for name, settings := range cases {
		if settings.Validate() == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	ok := DiscordNotifications{Events: []string{NotifySuccess}, MentionOnFailure: "<@123456789012345678>"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if !(DiscordNotifications{Enabled: true}).Wants(NotifyRestore) {
		t.Fatal("empty event list must mean all events")
	}
	if (DiscordNotifications{Enabled: true, Events: []string{NotifyFailure}}).Wants(NotifySuccess) {
		t.Fatal("unsubscribed event was wanted")
	}
	if (DiscordNotifications{Events: []string{NotifyFailure}}).Wants(NotifyFailure) {
		t.Fatal("disabled notifications must not want anything")
	}
}
