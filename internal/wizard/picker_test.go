package wizard

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
)

func pickerOptions(list func(context.Context, mysql.ToolInfo, config.Database, string) ([]string, error), tested *[]string) Options {
	return Options{
		DiscoverTools: fakeDiscover,
		ListDatabases: list,
		TestConnection: func(_ context.Context, _ mysql.ToolInfo, database config.Database, _ string) error {
			*tested = append(*tested, database.Database)
			return nil
		},
	}
}

func TestDatabasePickerOffersVisibleDatabases(t *testing.T) {
	var tested []string
	listed := 0
	options := pickerOptions(func(_ context.Context, _ mysql.ToolInfo, database config.Database, password string) ([]string, error) {
		listed++
		if database.Database != "" || password != "secret" {
			t.Fatalf("listing must not select a database: %#v", database)
		}
		return []string{"es_extended", "qbcore"}, nil
	}, &tested)
	// engine, TCP, host, port, user, password, pick the second database
	script := "\n2\n\n\n\nsecret\n2\n"
	var output bytes.Buffer
	profile, password, err := configureDatabase(context.Background(), NewPrompter(strings.NewReader(script), &output), options, config.Profile{}, "")
	if err != nil {
		t.Fatalf("configureDatabase: %v\n%s", err, output.String())
	}
	if profile.Database.Database != "qbcore" || password != "secret" || listed != 1 {
		t.Fatalf("database=%q password=%q listed=%d", profile.Database.Database, password, listed)
	}
	if len(tested) != 1 || tested[0] != "qbcore" {
		t.Fatalf("access check ran for %#v", tested)
	}
	if !strings.Contains(output.String(), "es_extended") || strings.Contains(output.String(), "secret") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestDatabasePickerOtherAllowsTypedName(t *testing.T) {
	var tested []string
	options := pickerOptions(func(context.Context, mysql.ToolInfo, config.Database, string) ([]string, error) {
		return []string{"es_extended"}, nil
	}, &tested)
	script := "\n2\n\n\n\nsecret\n2\nhidden_db\n"
	var output bytes.Buffer
	profile, _, err := configureDatabase(context.Background(), NewPrompter(strings.NewReader(script), &output), options, config.Profile{}, "")
	if err != nil {
		t.Fatalf("configureDatabase: %v\n%s", err, output.String())
	}
	if profile.Database.Database != "hidden_db" {
		t.Fatalf("database = %q", profile.Database.Database)
	}
}

func TestDatabaseListingAuthFailureOffersReview(t *testing.T) {
	var tested []string
	attempts := 0
	options := pickerOptions(func(context.Context, mysql.ToolInfo, config.Database, string) ([]string, error) {
		attempts++
		if attempts == 1 {
			return nil, &mysql.CommandError{Operation: "connection test", ExitCode: 1, Err: errors.New("exit 1"), Detail: "ERROR 1045 (28000): Access denied"}
		}
		return []string{"fxserver"}, nil
	}, &tested)
	// first attempt with a wrong password, review, then succeed
	script := "\n2\n\n\n\nwrong\ny\n2\n\n\n\nright\n\n"
	var output bytes.Buffer
	profile, password, err := configureDatabase(context.Background(), NewPrompter(strings.NewReader(script), &output), options, config.Profile{}, "")
	if err != nil {
		t.Fatalf("configureDatabase: %v\n%s", err, output.String())
	}
	if attempts != 2 || password != "right" || profile.Database.Database != "fxserver" {
		t.Fatalf("attempts=%d password=%q database=%q", attempts, password, profile.Database.Database)
	}
	if !strings.Contains(output.String(), "Review connection details?") {
		t.Fatalf("authentication failure was not reported:\n%s", output.String())
	}
}

func TestCompressionPresetIndexPreselectsSavedLevel(t *testing.T) {
	cases := map[int]int{0: 1, 1: 0, 3: 1, 9: 2, 6: 3, 19: 3}
	for level, want := range cases {
		if got := compressionPresetIndex(level); got != want {
			t.Errorf("compressionPresetIndex(%d) = %d, want %d", level, got, want)
		}
	}
}

func TestShortVersionExtractsRelease(t *testing.T) {
	got := shortVersion("mariadb-dump from 11.8.6-MariaDB, client 10.19 for Linux (x86_64)")
	if got != "11.8.6-MariaDB" {
		t.Fatalf("shortVersion = %q", got)
	}
	windows := shortVersion(`C:/Program Files/MariaDB 12.2/bin/mariadb-dump.exe from 12.2.2-MariaDB, client 10.19 for Win64 (AMD64)`)
	if windows != "12.2.2-MariaDB" {
		t.Fatalf("shortVersion(windows path) = %q", windows)
	}
	if got := shortVersion("mysqldump  Ver 8.0.36 for Win64 on x86_64 (MySQL Community Server - GPL)"); got != "8.0.36" {
		t.Fatalf("shortVersion(mysql) = %q", got)
	}
	if shortVersion("custom") != "custom" {
		t.Fatal("unparseable versions must be returned unchanged")
	}
}

func TestPlainInputStripsByteOrderMark(t *testing.T) {
	var output bytes.Buffer
	prompt := NewPrompter(strings.NewReader(byteOrderMark+"e2e\nsecond\n"), &output)
	first, err := prompt.Text("Profile name", "production", nil)
	if err != nil || first != "e2e" {
		t.Fatalf("first answer = %q, %v", first, err)
	}
	second, err := prompt.Text("Next", "", nil)
	if err != nil || second != "second" {
		t.Fatalf("second answer = %q, %v", second, err)
	}
}

func TestCloneHelpersKeepNotificationSettings(t *testing.T) {
	cfg := config.Config{
		Version:  config.CurrentVersion,
		Profiles: map[string]config.Profile{"production": {}},
		Notifications: config.Notifications{Discord: config.DiscordNotifications{
			Enabled: true,
			Events:  []string{config.NotifyFailure},
		}},
	}
	clone := cloneConfig(cfg)
	if !clone.Notifications.Discord.Enabled || len(clone.Notifications.Discord.Events) != 1 {
		t.Fatalf("notification settings were dropped: %#v", clone.Notifications)
	}
	clone.Notifications.Discord.Events[0] = config.NotifySuccess
	if cfg.Notifications.Discord.Events[0] != config.NotifyFailure {
		t.Fatal("clone shares the events slice with the original")
	}
	credentials := config.CredentialFile{
		Profiles:      map[string]config.Credential{"production": {Password: "x"}},
		Notifications: config.NotificationCredential{DiscordWebhookURL: "https://discord.com/api/webhooks/1/abc"},
	}
	if cloneCredentials(credentials).Notifications.DiscordWebhookURL == "" {
		t.Fatal("webhook URL was dropped when cloning credentials")
	}
}
