package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/history"
)

const testWebhook = "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz_0123456789-secret"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}
}

func testDiscord(transport roundTripFunc, settings config.DiscordNotifications) (*Discord, *[]time.Duration) {
	var sleeps []time.Duration
	discord := NewDiscord(testWebhook, settings)
	discord.Client = &http.Client{Transport: transport}
	discord.Sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}
	return discord, &sleeps
}

func TestValidateWebhookURL(t *testing.T) {
	valid := []string{
		testWebhook,
		"https://discordapp.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
		"https://discord.com/api/v10/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
	}
	for _, url := range valid {
		if err := ValidateWebhookURL(url); err != nil {
			t.Errorf("ValidateWebhookURL(%q) = %v", url, err)
		}
	}
	invalid := []string{
		"",
		"http://discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
		"https://evil.example/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
		"https://discord.com.evil.example/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
		"https://discord.com/channels/123/456",
		"https://user:pass@discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz",
		"https://discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz?thread_id=1",
	}
	for _, url := range invalid {
		if ValidateWebhookURL(url) == nil {
			t.Errorf("ValidateWebhookURL(%q) accepted an invalid URL", url)
		}
	}
}

func TestMaskWebhookURLHidesToken(t *testing.T) {
	masked := MaskWebhookURL(testWebhook)
	if strings.Contains(masked, "secret") || !strings.Contains(masked, "123456789012345678") {
		t.Fatalf("MaskWebhookURL = %q", masked)
	}
}

func decodePayload(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestPayloadNeverPingsFromErrorText(t *testing.T) {
	discord := NewDiscord(testWebhook, config.DiscordNotifications{})
	data, err := discord.Payload(Event{
		Kind:  BackupFailed,
		Title: "Backup failed",
		Error: "@everyone ``` injected ``` <@&123456789012345678>",
		Time:  time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodePayload(t, data)
	allowed := payload["allowed_mentions"].(map[string]any)
	if parse := allowed["parse"].([]any); len(parse) != 0 {
		t.Fatalf("allowed_mentions.parse = %v", parse)
	}
	if _, ok := payload["content"]; ok {
		t.Fatal("content must be empty without a configured mention")
	}
	description := payload["embeds"].([]any)[0].(map[string]any)["description"].(string)
	if strings.Count(description, "```") != 2 {
		t.Fatalf("error text broke out of its code block: %q", description)
	}
}

func TestPayloadMentionsConfiguredRoleOnFailureOnly(t *testing.T) {
	discord := NewDiscord(testWebhook, config.DiscordNotifications{MentionOnFailure: "<@&123456789012345678>"})
	failure, _ := discord.Payload(Event{Kind: BackupFailed, Title: "Backup failed", Time: time.Now()})
	payload := decodePayload(t, failure)
	if payload["content"] != "<@&123456789012345678>" {
		t.Fatalf("content = %v", payload["content"])
	}
	roles := payload["allowed_mentions"].(map[string]any)["roles"].([]any)
	if len(roles) != 1 || roles[0] != "123456789012345678" {
		t.Fatalf("roles = %v", roles)
	}
	success, _ := discord.Payload(Event{Kind: BackupSucceeded, Title: "Backup succeeded", Time: time.Now()})
	if _, ok := decodePayload(t, success)["content"]; ok {
		t.Fatal("success messages must not mention anyone")
	}
}

func TestPayloadRespectsDiscordLimits(t *testing.T) {
	discord := NewDiscord(testWebhook, config.DiscordNotifications{ServerName: "eu-1"})
	fields := make([]Field, 30)
	for index := range fields {
		fields[index] = Field{Name: strings.Repeat("n", 300), Value: strings.Repeat("v", 2000)}
	}
	data, err := discord.Payload(Event{Kind: BackupSucceeded, Title: strings.Repeat("t", 400), Fields: fields, Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	embed := decodePayload(t, data)["embeds"].([]any)[0].(map[string]any)
	if len([]rune(embed["title"].(string))) > maxTitle {
		t.Fatal("title exceeds the Discord limit")
	}
	gotFields := embed["fields"].([]any)
	if len(gotFields) > maxFields {
		t.Fatalf("%d fields exceed the Discord limit", len(gotFields))
	}
	first := gotFields[0].(map[string]any)
	if len([]rune(first["name"].(string))) > maxFieldName || len([]rune(first["value"].(string))) > maxFieldValue {
		t.Fatal("field exceeds the Discord limit")
	}
	if !strings.Contains(embed["footer"].(map[string]any)["text"].(string), "eu-1") {
		t.Fatal("server name missing from footer")
	}
}

func TestSendRetriesAfterRateLimit(t *testing.T) {
	calls := 0
	discord, sleeps := testDiscord(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s", request.Method, request.Header.Get("Content-Type"))
		}
		if calls == 1 {
			return response(http.StatusTooManyRequests, `{"retry_after": 0.25}`, nil), nil
		}
		return response(http.StatusNoContent, "", nil), nil
	}, config.DiscordNotifications{})
	if err := discord.Send(context.Background(), TestEvent(config.DiscordNotifications{})); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(*sleeps) != 1 || (*sleeps)[0] != 250*time.Millisecond {
		t.Fatalf("calls=%d sleeps=%v", calls, *sleeps)
	}
}

func TestSendErrorsNeverContainTheWebhook(t *testing.T) {
	cases := map[string]roundTripFunc{
		"network": func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: lookup " + request.URL.String() + " failed")
		},
		"not found": func(*http.Request) (*http.Response, error) {
			return response(http.StatusNotFound, `{"message": "Unknown Webhook"}`, nil), nil
		},
		"bad request": func(*http.Request) (*http.Response, error) {
			return response(http.StatusBadRequest, `{"message": "Invalid Form Body"}`, nil), nil
		},
	}
	for name, transport := range cases {
		discord, _ := testDiscord(transport, config.DiscordNotifications{})
		err := discord.Send(context.Background(), TestEvent(config.DiscordNotifications{}))
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "AbCdEf") {
			t.Fatalf("%s: error leaks the webhook token: %v", name, err)
		}
	}
}

func TestNotifierRespectsSubscriptions(t *testing.T) {
	sent := 0
	sender := senderFunc(func(context.Context, Event) error {
		sent++
		return nil
	})
	notifier := &Notifier{Settings: config.DiscordNotifications{Enabled: true, Events: []string{config.NotifyFailure}}, Discord: sender}
	_ = notifier.Notify(Event{Kind: BackupSucceeded})
	_ = notifier.Notify(Event{Kind: ConfigChanged})
	_ = notifier.Notify(Event{Kind: BackupFailed})
	if sent != 1 {
		t.Fatalf("sent %d events, want only the failure", sent)
	}
	var nilNotifier *Notifier
	if err := nilNotifier.Notify(Event{Kind: BackupFailed}); err != nil {
		t.Fatal("nil notifier must be a no-op")
	}
	if FromConfig(config.Config{}, config.CredentialFile{}) != nil {
		t.Fatal("unconfigured notifications must produce a nil notifier")
	}
}

type senderFunc func(context.Context, Event) error

func (f senderFunc) Send(ctx context.Context, event Event) error { return f(ctx, event) }

func TestBackupEventDescribesOutcome(t *testing.T) {
	profile := config.Profile{Database: config.Database{Database: "qbcore", Host: "127.0.0.1", Port: 3306}}
	success := BackupEvent("production", profile, history.Record{
		ID: "run-1", Status: history.StatusSuccess, Archive: "/backups/production-1.sql.zst",
		ArchiveSize: 3 * 1024 * 1024, UncompressedSize: 40 * 1024 * 1024, DurationMS: 1500, Verified: true,
		RetentionDeleted: 2, RetentionFreedBytes: 6 * 1024 * 1024, FinishedAt: time.Now(),
	})
	if success.Kind != BackupSucceeded || !hasField(success, "Retention") || !hasField(success, "Size") {
		t.Fatalf("unexpected success event: %#v", success)
	}
	failure := BackupEvent("production", profile, history.Record{
		ID: "run-2", Status: history.StatusFailed, Phase: "dumping", Error: "Access denied", FinishedAt: time.Now(),
	})
	if failure.Kind != BackupFailed || failure.Error != "Access denied" || !hasField(failure, "Full log") {
		t.Fatalf("unexpected failure event: %#v", failure)
	}
}

func hasField(event Event, name string) bool {
	for _, field := range event.Fields {
		if field.Name == name {
			return true
		}
	}
	return false
}
