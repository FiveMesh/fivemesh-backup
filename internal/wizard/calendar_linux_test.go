//go:build linux

package wizard

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGeneratedCalendarsWithSystemdAnalyze(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	cases := []struct {
		name       string
		hours      int
		clock      string
		normalized string
	}{
		{name: "six-hour arbitrary", hours: 6, clock: "06:30", normalized: "*-*-* 00,06,12,18:30:00 Europe/Brussels"},
		{name: "twelve-hour arbitrary", hours: 12, clock: "23:45", normalized: "*-*-* 11,23:45:00 Europe/Brussels"},
		{name: "daily", hours: 24, clock: "06:00", normalized: "*-*-* 06:00:00 Europe/Brussels"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			calendar := calendarForInterval(testCase.hours, testCase.clock) + " Europe/Brussels"
			output, err := exec.Command(
				analyze,
				"calendar",
				"--iterations=3",
				calendar,
			).CombinedOutput()
			if err != nil {
				t.Fatalf("systemd rejected %q: %v\n%s", calendar, err, output)
			}
			if !strings.Contains(string(output), "Normalized form: "+testCase.normalized) {
				t.Fatalf("unexpected normalized calendar:\n%s", output)
			}
		})
	}
}

func TestSystemdRejectsLegacyCommaSeparatedTimeValues(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	legacy := "*-*-* 00:00:00,06:00:00,12:00:00,18:00:00 Europe/Brussels"
	if output, err := exec.Command(analyze, "calendar", legacy).CombinedOutput(); err == nil {
		t.Fatalf("systemd unexpectedly accepted legacy expression: %s", output)
	}
}

func TestCalendarMaintainsLocalTimeAcrossDST(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	calendar := calendarForInterval(24, "06:00") + " Europe/Brussels"
	spring, err := exec.Command(
		analyze,
		"calendar",
		"--iterations=3",
		"--base-time=2026-03-28 00:00:00 UTC",
		calendar,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("spring transition validation failed: %v\n%s", err, spring)
	}
	for _, expected := range []string{
		"Sat 2026-03-28 05:00:00 UTC",
		"Sun 2026-03-29 04:00:00 UTC",
	} {
		if !strings.Contains(string(spring), expected) {
			t.Fatalf("spring DST output is missing %q:\n%s", expected, spring)
		}
	}

	autumn, err := exec.Command(
		analyze,
		"calendar",
		"--iterations=3",
		"--base-time=2026-10-24 00:00:00 UTC",
		calendar,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("autumn transition validation failed: %v\n%s", err, autumn)
	}
	for _, expected := range []string{
		"Sat 2026-10-24 04:00:00 UTC",
		"Sun 2026-10-25 05:00:00 UTC",
	} {
		if !strings.Contains(string(autumn), expected) {
			t.Fatalf("autumn DST output is missing %q:\n%s", expected, autumn)
		}
	}
}
