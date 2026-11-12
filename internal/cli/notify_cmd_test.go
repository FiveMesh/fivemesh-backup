package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
)

const cliTestWebhook = "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz_secret-token"

func notifyTestPaths(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config.toml"),
		Credentials: filepath.Join(root, "credentials.toml"),
		StateDir:    filepath.Join(root, "state"),
	}
	cfg := config.Config{
		Version:        config.CurrentVersion,
		DefaultProfile: "production",
		Profiles: map[string]config.Profile{"production": {
			Database: config.Database{
				Engine: "mysql", DumpTool: "auto", ClientTool: "auto",
				Host: "127.0.0.1", Port: 3306, Username: "backup", Database: "fxserver",
			},
			Backup:   config.Backup{Directory: filepath.Join(root, "backups"), CompressionLevel: 3},
			Schedule: config.Schedule{Mode: config.ScheduleModeManual},
		}},
	}
	credentials := config.CredentialFile{Profiles: map[string]config.Credential{"production": {Password: "db-secret"}}}
	if err := config.SaveInitial(cfg, credentials, paths); err != nil {
		t.Fatal(err)
	}
	return paths
}

func runNotifyCommand(t *testing.T, paths config.Paths, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"notify"}, args...)
	full = append(full, "--config", paths.Config, "--credentials", paths.Credentials, "--state-dir", paths.StateDir)
	code := Run(context.Background(), full, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func stubNotifySend(t *testing.T, err error) *[]notify.Event {
	t.Helper()
	var sent []notify.Event
	previous := notifySend
	notifySend = func(_ context.Context, webhook string, _ config.DiscordNotifications, event notify.Event) error {
		if webhook != cliTestWebhook {
			t.Errorf("unexpected webhook %q", webhook)
		}
		sent = append(sent, event)
		return err
	}
	t.Cleanup(func() { notifySend = previous })
	return &sent
}

func TestNotifyDiscordScriptedSetup(t *testing.T) {
	paths := notifyTestPaths(t)
	sent := stubNotifySend(t, nil)
	code, stdout, stderr := runNotifyCommand(t, paths, cliTestWebhook+"\n",
		"discord", "--webhook-url-stdin", "--events", "failure,config", "--server-name", "eu-1", "--mention", "<@&123456789012345678>")
	if code != exitSuccess {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(*sent) != 1 || (*sent)[0].Kind != notify.Test {
		t.Fatalf("expected one test message, got %#v", *sent)
	}
	if strings.Contains(stdout+stderr, "secret-token") {
		t.Fatal("webhook token was printed")
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	discord := cfg.Notifications.Discord
	if !discord.Enabled || discord.ServerName != "eu-1" || strings.Join(discord.Events, ",") != "failure,config" {
		t.Fatalf("unexpected settings: %#v", discord)
	}
	credentials, err := config.LoadCredentials(paths.Credentials)
	if err != nil || credentials.Notifications.DiscordWebhookURL != cliTestWebhook {
		t.Fatalf("webhook not stored in credentials: %v", err)
	}
	if credentials.Profiles["production"].Password != "db-secret" {
		t.Fatal("database password was lost while saving notifications")
	}
	data, _ := os.ReadFile(paths.Config)
	if strings.Contains(string(data), "webhooks") {
		t.Fatal("webhook URL leaked into config.toml")
	}
}

func TestNotifyDiscordRejectsBadInputWithoutSaving(t *testing.T) {
	paths := notifyTestPaths(t)
	stubNotifySend(t, errors.New("Discord rejected the webhook"))
	cases := [][]string{
		{"https://example.com/hook\n", "discord", "--webhook-url-stdin"},
		{cliTestWebhook + "\n", "discord", "--webhook-url-stdin", "--events", "everything"},
		{cliTestWebhook + "\n", "discord", "--webhook-url-stdin"}, // test message fails
	}
	for _, testCase := range cases {
		code, _, _ := runNotifyCommand(t, paths, testCase[0], testCase[1:]...)
		if code == exitSuccess {
			t.Fatalf("%v unexpectedly succeeded", testCase[1:])
		}
		credentials, _ := config.LoadCredentials(paths.Credentials)
		if credentials.Notifications.DiscordWebhookURL != "" {
			t.Fatalf("%v saved a webhook despite failing", testCase[1:])
		}
	}
}

func TestNotifyStatusEnableDisableRemove(t *testing.T) {
	paths := notifyTestPaths(t)
	stubNotifySend(t, nil)
	if _, stdout, _ := runNotifyCommand(t, paths, "", "status"); !strings.Contains(stdout, "not configured") {
		t.Fatalf("status before setup: %s", stdout)
	}
	if code, _, _ := runNotifyCommand(t, paths, "", "enable"); code == exitSuccess {
		t.Fatal("enable without a webhook must fail")
	}
	if code, _, stderr := runNotifyCommand(t, paths, cliTestWebhook+"\n", "discord", "--webhook-url-stdin", "--no-test"); code != exitSuccess {
		t.Fatalf("setup failed: %s", stderr)
	}
	_, stdout, _ := runNotifyCommand(t, paths, "", "status")
	if !strings.Contains(stdout, "enabled") || !strings.Contains(stdout, "123456789012345678") || strings.Contains(stdout, "secret-token") {
		t.Fatalf("status output: %s", stdout)
	}
	runNotifyCommand(t, paths, "", "disable")
	if cfg, _ := config.Load(paths.Config); cfg.Notifications.Discord.Enabled {
		t.Fatal("disable did not persist")
	}
	runNotifyCommand(t, paths, "", "enable")
	if cfg, _ := config.Load(paths.Config); !cfg.Notifications.Discord.Enabled {
		t.Fatal("enable did not persist")
	}
	runNotifyCommand(t, paths, "", "remove")
	credentials, _ := config.LoadCredentials(paths.Credentials)
	cfg, _ := config.Load(paths.Config)
	if credentials.Notifications.DiscordWebhookURL != "" || cfg.Notifications.Discord.Enabled {
		t.Fatal("remove did not clear notification settings")
	}
}

func TestSendNotificationWarnsWithoutFailing(t *testing.T) {
	previous := notifyLoader
	notifyLoader = func(pathFlags) *notify.Notifier {
		return &notify.Notifier{
			Settings: config.DiscordNotifications{Enabled: true},
			Discord:  failingSender{},
		}
	}
	t.Cleanup(func() { notifyLoader = previous })
	var stderr bytes.Buffer
	sendNotification(pathFlags{}, notify.Event{Kind: notify.BackupFailed}, &stderr)
	if !strings.Contains(stderr.String(), "warning: Discord notification was not delivered") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

type failingSender struct{}

func (failingSender) Send(context.Context, notify.Event) error {
	return errors.New("Discord is unavailable")
}

func TestNotifyDiscordStdinAcceptsDoublePastedWebhook(t *testing.T) {
	paths := notifyTestPaths(t)
	stubNotifySend(t, nil)
	code, _, stderr := runNotifyCommand(t, paths, cliTestWebhook+cliTestWebhook+"\n", "discord", "--webhook-url-stdin")
	if code != exitSuccess {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	credentials, _ := config.LoadCredentials(paths.Credentials)
	if credentials.Notifications.DiscordWebhookURL != cliTestWebhook {
		t.Fatalf("stored webhook = %q", credentials.Notifications.DiscordWebhookURL)
	}
}
