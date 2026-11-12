package notify

import (
	"strings"
	"testing"
)

func TestNormalizeWebhookURLAcceptsCommonPastes(t *testing.T) {
	cases := map[string]struct {
		input      string
		duplicated bool
	}{
		"plain":                     {testWebhook, false},
		"surrounding whitespace":    {"  " + testWebhook + " \r\n", false},
		"angle brackets":            {"<" + testWebhook + ">", false},
		"quotes":                    {`"` + testWebhook + `"`, false},
		"pasted twice glued":        {testWebhook + testWebhook, true},
		"pasted twice with a space": {testWebhook + " " + testWebhook, true},
		"pasted three times":        {strings.Repeat(testWebhook, 3), true},
		"uppercase host":            {strings.Replace(testWebhook, "discord.com", "Discord.com", 1), false},
		"canary host":               {strings.Replace(testWebhook, "discord.com", "canary.discord.com", 1), false},
		"versioned api":             {strings.Replace(testWebhook, "/api/", "/api/v10/", 1), false},
	}
	for name, testCase := range cases {
		got, duplicated, err := NormalizeWebhookURL(testCase.input)
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
			continue
		}
		if ValidateWebhookURL(got) != nil || strings.Contains(got, " ") || strings.Count(got, "https://") != 1 {
			t.Errorf("%s: normalized URL is not a single valid webhook: %q", name, got)
		}
		if duplicated != testCase.duplicated {
			t.Errorf("%s: duplicated = %v, want %v", name, duplicated, testCase.duplicated)
		}
	}
}

func TestNormalizeWebhookURLRejectsAmbiguousInput(t *testing.T) {
	other := strings.Replace(testWebhook, "123456789012345678", "876543210987654321", 1)
	cases := map[string]struct {
		input   string
		message string
	}{
		"two different webhooks": {testWebhook + other, "two different"},
		"thread query":           {testWebhook + "?thread_id=123", "thread_id"},
		"trailing text":          {testWebhook + " please", "extra text"},
		"not discord":            {"https://example.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyz", "discord.com"},
		"truncated token":        {"https://discord.com/api/webhooks/123456789012345678/short", "does not look like"},
		"two non-webhook URLs":   {"https://discord.com/channels/1/2https://discord.com/channels/3/4", "more than one URL"},
	}
	for name, testCase := range cases {
		_, _, err := NormalizeWebhookURL(testCase.input)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: error %q does not mention %q", name, err, testCase.message)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error leaks the token: %v", name, err)
		}
	}
}
