package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestMapTeamsPresence(t *testing.T) {
	tests := []struct {
		availability    string
		wantPresence    event.Presence
		wantStatusMsg   string
	}{
		{"Available", event.PresenceOnline, ""},
		{"Busy", event.PresenceOnline, "Busy"},
		{"DoNotDisturb", event.PresenceUnavailable, "Do Not Disturb"},
		{"Away", event.PresenceUnavailable, "Away"},
		{"BeRightBack", event.PresenceUnavailable, "Be Right Back"},
		{"Offline", event.PresenceOffline, ""},
		{"Unknown", event.PresenceOffline, ""},
		{"", event.PresenceOffline, ""},
	}

	for _, tt := range tests {
		t.Run(tt.availability, func(t *testing.T) {
			presence, statusMsg := mapTeamsPresence(tt.availability)
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
		presenceCache: make(map[string]string),
	}

	// First update should be recorded.
	c.presenceMu.Lock()
	c.presenceCache["user-1"] = "Available"
	c.presenceMu.Unlock()

	c.presenceMu.Lock()
	prev, hasPrev := c.presenceCache["user-1"]
	c.presenceMu.Unlock()

	if !hasPrev || prev != "Available" {
		t.Errorf("expected cached presence 'Available', got %q (exists=%v)", prev, hasPrev)
	}
}
