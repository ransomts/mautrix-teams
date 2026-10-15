package connector

import (
	"testing"
	"time"
)

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for arg, want := range map[string]time.Time{
		"":                          {},
		"90m":                       now.Add(90 * time.Minute),
		"2026-10-14T23:59:00-04:00": time.Date(2026, 10, 15, 3, 59, 0, 0, time.UTC),
	} {
		got, err := parseExpiry(arg, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseExpiry(%q) = %v, %v; want %v", arg, got, err, want)
		}
	}
	for _, bad := range []string{"-1h", "soon", "2026-10-01T00:00:00Z"} {
		if _, err := parseExpiry(bad, now); err == nil {
			t.Errorf("parseExpiry(%q) succeeded", bad)
		}
	}
	for word, availability := range map[string]string{"dnd": "DoNotDisturb", "brb": "BeRightBack", "reset": ""} {
		if statusWords[word] != availability {
			t.Errorf("statusWords[%q] = %q", word, statusWords[word])
		}
	}
}

func TestExpiryPhrase(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 1, 0, 0, time.UTC)
	if got := expiryPhrase("30m", now.Add(30*time.Minute)); got != " for 30m" {
		t.Errorf("duration: %q", got)
	}
	until, _ := parseExpiry("2026-10-14T23:59:59-04:00", time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if got := expiryPhrase("2026-10-14T23:59:59-04:00", until); got != " until Wed 23:59" {
		t.Errorf("time: %q", got)
	}
	if got := expiryPhrase("", time.Time{}); got != "" {
		t.Errorf("none: %q", got)
	}
}
