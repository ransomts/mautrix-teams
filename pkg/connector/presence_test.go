package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/presence"

	"maunium.net/go/mautrix/event"
)

func TestMapTeamsPresence(t *testing.T) {
	tests := []struct {
		availability  string
		activity      string
		wantPresence  event.Presence
		wantStatusMsg string
	}{
		{"Available", "Available", event.PresenceOnline, ""},
		{"Busy", "Busy", event.PresenceOnline, "Busy"},
		{"Busy", "InAMeeting", event.PresenceOnline, "In a meeting"},
		{"Busy", "InACall", event.PresenceOnline, "In a call"},
		{"DoNotDisturb", "Presenting", event.PresenceUnavailable, "Presenting"},
		{"DoNotDisturb", "DoNotDisturb", event.PresenceUnavailable, "Do Not Disturb"},
		{"Away", "Away", event.PresenceUnavailable, "Away"},
		{"AvailableIdle", "Inactive", event.PresenceUnavailable, "Away"},
		{"BeRightBack", "BeRightBack", event.PresenceUnavailable, "Be Right Back"},
		{"Offline", "Offline", event.PresenceOffline, ""},
		{"Offline", "OutOfOffice", event.PresenceOffline, "Out of office"},
		{"PresenceUnknown", "", event.PresenceOffline, ""},
		{"", "", event.PresenceOffline, ""},
	}

	for _, tt := range tests {
		t.Run(tt.availability+"/"+tt.activity, func(t *testing.T) {
			presence, statusMsg := mapTeamsPresence(tt.availability, tt.activity)
			if presence != tt.wantPresence {
				t.Errorf("mapTeamsPresence(%q) presence = %s, want %s", tt.availability, presence, tt.wantPresence)
			}
			if statusMsg != tt.wantStatusMsg {
				t.Errorf("mapTeamsPresence(%q) statusMsg = %q, want %q", tt.availability, statusMsg, tt.wantStatusMsg)
			}
		})
	}
}

func TestTrackKnownUser(t *testing.T) {
	c := &TeamsClient{}

	c.trackKnownUser("user-1")
	c.trackKnownUser("user-2")
	c.trackKnownUser("user-1") // duplicate

	c.knownUsersMu.Lock()
	defer c.knownUsersMu.Unlock()
	if len(c.knownUsers) != 2 {
		t.Errorf("expected 2 known users, got %d", len(c.knownUsers))
	}
}

func TestTrackKnownUser_Empty(t *testing.T) {
	c := &TeamsClient{}
	c.trackKnownUser("") // should be a no-op

	c.knownUsersMu.Lock()
	defer c.knownUsersMu.Unlock()
	if c.knownUsers != nil {
		t.Error("expected nil knownUsers for empty input")
	}
}

func TestPresenceCache(t *testing.T) {
	c := &TeamsClient{
		presenceCache: make(map[string]presence.Presence),
	}

	// First update should be recorded.
	c.presenceMu.Lock()
	c.presenceCache["user-1"] = presence.Presence{Availability: "Available"}
	c.presenceMu.Unlock()

	c.presenceMu.Lock()
	prev, hasPrev := c.presenceCache["user-1"]
	c.presenceMu.Unlock()

	if !hasPrev || prev.Availability != "Available" {
		t.Errorf("expected cached presence 'Available', got %+v (exists=%v)", prev, hasPrev)
	}
}

// An empty status message is sent, not left out, so it clears the old one.
func TestSetPresenceSendsEmptyStatusMsg(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/_matrix/client/v3/presence/@ghost:localhost/status" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	cli, err := mautrix.NewClient(srv.URL, id.UserID("@ghost:localhost"), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if err := setPresence(context.Background(), cli, event.PresenceOffline, ""); err != nil {
		t.Fatal(err)
	}
	if msg, ok := body["status_msg"]; !ok || msg != "" || body["presence"] != "offline" {
		t.Errorf("body = %v, want presence offline and an empty status_msg", body)
	}
}

func TestPresenceStatusNotes(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	p := presence.Presence{
		Availability: "Away", Activity: "Away",
		Note: presence.Note{Message: "Office hours 2&ndash;4 in &lt;b&gt;McAdams&lt;/b&gt;<pinnednote></pinnednote>"},
		CalendarData: presence.CalendarData{IsOutOfOffice: true,
			OutOfOfficeNote: presence.Note{Message: "<p>Back Monday</p>", Expiry: "2026-10-20T00:00:00Z"}},
		WorkLocation: presence.WorkLocation{Location: "2"},
	}
	mp, msg := presenceStatus(p, now)
	if mp != event.PresenceUnavailable || msg != "Away\nNote: Office hours 2–4 in <b>McAdams</b>\nOut of office: Back Monday\nLocation: Remote" {
		t.Errorf("got %s %q", mp, msg)
	}
	// Expired notes are dropped; plain available has an empty first line only when a note follows.
	p = presence.Presence{Availability: "Available", Note: presence.Note{Message: "old", Expiry: "2026-10-01T00:00:00Z"}}
	if _, msg := presenceStatus(p, now); msg != "" {
		t.Errorf("expired note: got %q", msg)
	}
	p.Note.Expiry = ""
	if _, msg := presenceStatus(p, now); msg != "\nNote: old" {
		t.Errorf("note on available: got %q", msg)
	}
}
