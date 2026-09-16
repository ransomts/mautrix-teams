package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestTeamSpaceChatInfoIgnoresGenericChat(t *testing.T) {
	c := &TeamsClient{}
	// A stored "Chat" is the generic fallback, not a real name.
	info := c.teamSpaceChatInfo(context.Background(), "team1", &bridgev2.Portal{Portal: &database.Portal{Name: "Chat"}})
	if *info.Name != "Team" {
		t.Fatalf("stored \"Chat\" should be ignored, got %q", *info.Name)
	}
	// A cached team name (from discovery) wins even over a real stored name.
	c.cacheTeamNames(map[string]string{"team1": "CPSC 1050/1051 SP26"})
	info = c.teamSpaceChatInfo(context.Background(), "team1", &bridgev2.Portal{Portal: &database.Portal{Name: "Chat"}})
	if *info.Name != "CPSC 1050/1051 SP26" {
		t.Fatalf("cached team name should be used, got %q", *info.Name)
	}
	if info.Type == nil || *info.Type != database.RoomTypeSpace {
		t.Fatalf("team portals must be spaces")
	}
}

func TestCacheTeamNamesMergesNonEmpty(t *testing.T) {
	c := &TeamsClient{}
	c.cacheTeamNames(map[string]string{"a": "Alpha", "b": ""})
	if c.cachedTeamName("a") != "Alpha" {
		t.Fatalf("expected Alpha")
	}
	if c.cachedTeamName("b") != "" {
		t.Fatalf("empty name should not be cached")
	}
	// A later non-empty value fills the gap; an empty one never overwrites.
	c.cacheTeamNames(map[string]string{"b": "Beta"})
	c.cacheTeamNames(map[string]string{"a": ""})
	if c.cachedTeamName("b") != "Beta" || c.cachedTeamName("a") != "Alpha" {
		t.Fatalf("cache merge wrong: a=%q b=%q", c.cachedTeamName("a"), c.cachedTeamName("b"))
	}
}

func TestSystemStreamNameCoversDraftsAndMentions(t *testing.T) {
	cases := map[string]string{
		"19:teamsstream_drafts_uid@thread.v2":        "Drafts",
		"19:teamsstream_mentions_uid@thread.v2":      "Mentions",
		"19:teamsstream_notifications_uid@thread.v2": "Notifications",
		"19:teamsstream_calllogs_uid@thread.v2":      "Call Log",
		"19:teamsstream_notes_uid@thread.v2":         "Notes",
		"19:abc@thread.tacv2":                        "",
	}
	for id, want := range cases {
		if got := systemStreamName(id); got != want {
			t.Errorf("systemStreamName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSystemStreamsAreNotPollableExceptNotes(t *testing.T) {
	// refreshThreads skips creating portals for these (except notes).
	if !isNonPollableSystemStream("19:teamsstream_drafts_uid@thread.v2") {
		t.Fatal("drafts should be treated as a non-real system stream")
	}
	if !isNonPollableSystemStream("19:teamsstream_mentions_uid@thread.v2") {
		t.Fatal("mentions should be treated as a non-real system stream")
	}
	if isNonPollableSystemStream("19:teamsstream_notes_uid@thread.v2") {
		t.Fatal("notes is a real chat and must not be skipped")
	}
}
