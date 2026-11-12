package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
)

// Embed colours. Status colours are functional: they let people scan a busy
// channel at a glance.
const (
	colorSuccess = 0x3BA55C
	colorFailure = 0xED4245
	colorInfo    = 0x99AAB5
)

// Discord limits (https://discord.com/developers/docs/resources/message#embed-object-embed-limits).
const (
	maxTitle       = 256
	maxDescription = 4096
	maxFieldName   = 256
	maxFieldValue  = 1024
	maxFields      = 25
	maxFooter      = 2048
)

var webhookPath = regexp.MustCompile(`^/api/(v[0-9]+/)?webhooks/[0-9]{15,22}/[A-Za-z0-9_.-]{20,}$`)

// ValidateWebhookURL accepts only Discord webhook URLs over HTTPS. It keeps
// typos and unrelated URLs out of the credentials file.
func ValidateWebhookURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("enter the full webhook URL from Discord (Server Settings → Integrations → Webhooks → Copy Webhook URL)")
	}
	switch strings.ToLower(parsed.Host) {
	case "discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com":
	default:
		return errors.New("the webhook URL must start with https://discord.com/api/webhooks/")
	}
	if !webhookPath.MatchString(parsed.Path) {
		return errors.New("that does not look like a Discord webhook URL (expected https://discord.com/api/webhooks/<id>/<token>)")
	}
	return nil
}

var webhookInText = regexp.MustCompile(`(?i:https://(?:(?:ptb|canary)\.)?discord(?:app)?\.com)/api/(?:v[0-9]+/)?webhooks/[0-9]{15,22}/[A-Za-z0-9_.-]{20,}`)

// NormalizeWebhookURL extracts the webhook URL from pasted text. Terminals
// can deliver a paste twice (the terminal pastes and the input field also
// handles Ctrl+V), and people copy URLs with quotes or angle brackets.
func NormalizeWebhookURL(raw string) (webhook string, duplicated bool, err error) {
	// Separate URLs that were glued together ("...TOKENhttps://...").
	text := strings.ReplaceAll(raw, "https://", " https://")
	matches := webhookInText.FindAllString(text, -1)
	if len(matches) == 0 {
		trimmed := strings.Trim(strings.TrimSpace(raw), "<>\"'`")
		if strings.Count(strings.ToLower(trimmed), "https://") > 1 {
			return "", false, errors.New("the pasted text contains more than one URL; paste the webhook URL once")
		}
		return "", false, ValidateWebhookURL(trimmed)
	}
	for _, match := range matches[1:] {
		if match != matches[0] {
			return "", false, errors.New("two different webhook URLs were pasted; paste only the one you want to use")
		}
	}
	leftover := text
	for _, match := range matches {
		leftover = strings.Replace(leftover, match, "", 1)
	}
	leftover = strings.Trim(leftover, " \t\r\n<>\"'`")
	switch {
	case strings.HasPrefix(leftover, "?"):
		return "", false, errors.New("webhook URLs with options after \"?\" (such as thread_id) are not supported; use the plain webhook URL")
	case leftover != "":
		return "", false, errors.New("the webhook URL has extra text around it; paste only the URL copied from Discord")
	}
	if err := ValidateWebhookURL(matches[0]); err != nil {
		return "", false, err
	}
	return matches[0], len(matches) > 1, nil
}

// MaskWebhookURL shows enough of a webhook to recognise it without exposing
// its token.
func MaskWebhookURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "(invalid)"
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return "(invalid)"
	}
	id := parts[len(parts)-2]
	return fmt.Sprintf("https://%s/api/webhooks/%s/••••••", parsed.Host, id)
}

// Discord posts events to a webhook.
type Discord struct {
	WebhookURL string
	Settings   config.DiscordNotifications
	Client     *http.Client
	// Sleep is replaced in tests.
	Sleep func(context.Context, time.Duration) error
}

func NewDiscord(webhookURL string, settings config.DiscordNotifications) *Discord {
	return &Discord{
		WebhookURL: strings.TrimSpace(webhookURL),
		Settings:   settings,
		Client:     &http.Client{Timeout: 10 * time.Second},
		Sleep:      sleepContext,
	}
}

type discordPayload struct {
	Username        string          `json:"username"`
	Content         string          `json:"content,omitempty"`
	Embeds          []discordEmbed  `json:"embeds"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
	Roles []string `json:"roles,omitempty"`
	Users []string `json:"users,omitempty"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Footer      discordFooter  `json:"footer"`
	Timestamp   string         `json:"timestamp"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordFooter struct {
	Text string `json:"text"`
}

// Payload builds the webhook body. It is exported for tests and previews.
func (d *Discord) Payload(event Event) ([]byte, error) {
	color := colorInfo
	switch {
	case event.Kind.Failure():
		color = colorFailure
	case event.Kind == BackupSucceeded || event.Kind == RestoreSucceeded || event.Kind == Test:
		color = colorSuccess
	}
	title := event.Title
	if event.Profile != "" {
		title += " · " + event.Profile
	}
	description := event.Summary
	if event.Error != "" {
		block := "```\n" + strings.ReplaceAll(truncate(event.Error, 1500), "```", "'''") + "\n```"
		if description != "" {
			description += "\n"
		}
		description += block
	}
	embed := discordEmbed{
		Title:       truncate(title, maxTitle),
		Description: truncate(description, maxDescription),
		Color:       color,
		Footer:      discordFooter{Text: truncate(footerText(d.Settings.ServerName), maxFooter)},
		Timestamp:   event.Time.UTC().Format(time.RFC3339),
	}
	for index, field := range event.Fields {
		if index == maxFields {
			break
		}
		if strings.TrimSpace(field.Value) == "" {
			continue
		}
		embed.Fields = append(embed.Fields, discordField{
			Name:   truncate(field.Name, maxFieldName),
			Value:  truncate(field.Value, maxFieldValue),
			Inline: field.Inline,
		})
	}
	payload := discordPayload{
		Username: "FiveMesh Backup",
		Embeds:   []discordEmbed{embed},
		// Never let text from errors or database names ping anyone.
		AllowedMentions: allowedMentions{Parse: []string{}},
	}
	if event.Kind.Failure() && d.Settings.MentionOnFailure != "" {
		mention := d.Settings.MentionOnFailure
		payload.Content = mention
		id := strings.Trim(mention, "<@&>")
		if strings.HasPrefix(mention, "<@&") {
			payload.AllowedMentions.Roles = []string{id}
		} else {
			payload.AllowedMentions.Users = []string{id}
		}
	}
	return json.Marshal(payload)
}

// Send posts the event, retrying once after a rate limit or server error.
func (d *Discord) Send(ctx context.Context, event Event) error {
	if err := ValidateWebhookURL(d.WebhookURL); err != nil {
		return err
	}
	body, err := d.Payload(event)
	if err != nil {
		return fmt.Errorf("build Discord message: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		retryAfter, err := d.post(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if retryAfter <= 0 || attempt == 2 {
			break
		}
		if retryAfter > 5*time.Second {
			break
		}
		if sleepErr := d.Sleep(ctx, retryAfter); sleepErr != nil {
			break
		}
	}
	return lastErr
}

// post returns how long to wait before retrying, or zero if retrying is
// pointless. Errors never contain the webhook URL.
func (d *Discord) post(ctx context.Context, body []byte) (time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.WebhookURL+"?wait=false", bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("prepare Discord request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "FiveMesh-Backup (https://github.com/FiveMesh/fivemesh-backup)")
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		if ctx.Err() != nil {
			return 0, errors.New("Discord did not respond in time")
		}
		return time.Second, fmt.Errorf("could not reach Discord: %s", d.redact(err.Error()))
	}
	defer response.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return 0, nil
	case response.StatusCode == http.StatusTooManyRequests:
		return retryDelay(response.Header.Get("Retry-After"), detail), errors.New("Discord rate limit reached")
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusUnauthorized:
		return 0, errors.New("Discord rejected the webhook (it may have been deleted or regenerated); set it again with \"fivemesh-backup notify discord\"")
	case response.StatusCode >= 500:
		return 2 * time.Second, fmt.Errorf("Discord is unavailable (HTTP %d)", response.StatusCode)
	default:
		return 0, fmt.Errorf("Discord refused the message (HTTP %d): %s", response.StatusCode, d.redact(discordMessage(detail)))
	}
}

// redact removes the webhook URL and its token from text that may end up in
// logs or on screen.
func (d *Discord) redact(text string) string {
	text = strings.ReplaceAll(text, d.WebhookURL, "[webhook]")
	if parsed, err := url.Parse(d.WebhookURL); err == nil {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if token := parts[len(parts)-1]; len(token) >= 8 {
			text = strings.ReplaceAll(text, token, "[token]")
		}
	}
	return text
}

func retryDelay(header string, body []byte) time.Duration {
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && seconds >= 0 {
		return time.Duration(seconds*1000) * time.Millisecond
	}
	var parsed struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.RetryAfter > 0 {
		return time.Duration(parsed.RetryAfter*1000) * time.Millisecond
	}
	return time.Second
}

func discordMessage(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		return truncate(parsed.Message, 200)
	}
	return "no details"
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
