package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-teams/pkg/teamsid"
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

// Every bridge restart used to rename each channel three times ("Chat", a
// member list, then "Team / Channel") because the conversation list's
// generic name replaced the stored one.
func TestChooseStoredNameKeepsWorkedOutNames(t *testing.T) {
	cases := []struct{ api, existing, want string }{
		// First sighting.
		{"Chat", "", "Chat"},
		{"Alex", "", "Alex"},
		// Channel: the API only ever says "Chat"; the structured name stays.
		{"Chat", "CPSC 1050/1051 SP26 / General Student Questions", "CPSC 1050/1051 SP26 / General Student Questions"},
		// Channel whose conversation-list name is the bare channel name.
		{"Lab and Project Help", "CPSC 1050/1051 SP26 / Lab and Project Help", "CPSC 1050/1051 SP26 / Lab and Project Help"},
		// … but a channel renamed in Teams goes through.
		{"Lab Help", "CPSC 1050/1051 SP26 / Lab and Project Help", "Lab Help"},
		// System stream that is polled (notes).
		{"Chat", "Notes", "Notes"},
		// Unnamed group chat keeps its member-list name.
		{"Chat", "Group: Ahmad Sarlak, Shrinidhi Kulkarni", "Group: Ahmad Sarlak, Shrinidhi Kulkarni"},
		// DM: same person, prefixed form kept.
		{"Alex Adkins-Daniel", "DM: Alex Adkins-Daniel", "DM: Alex Adkins-Daniel"},
		// DM: the person changed their display name; the new one goes
		// through (and gets prefixed again later).
		{"Alex Adkins-Wright", "DM: Alex Adkins-Daniel", "Alex Adkins-Wright"},
		// DM whose counterpart is unknown keeps the placeholder until a
		// real name turns up.
		{"", "DM: unknown user 1ae9e827", "DM: unknown user 1ae9e827"},
		{"Real Name", "DM: unknown user 1ae9e827", "Real Name"},
		// A group chat given a title in Teams.
		{"Project X", "Group: Project X", "Group: Project X"},
		{"Project Y", "Group: Project X", "Project Y"},
		// Legacy bracket prefix.
		{"Alex", "[DM] Alex", "[DM] Alex"},
	}
	for _, tc := range cases {
		if got := chooseStoredName(tc.api, tc.existing); got != tc.want {
			t.Errorf("chooseStoredName(%q, %q) = %q, want %q", tc.api, tc.existing, got, tc.want)
		}
	}
}

func TestNameHelpers(t *testing.T) {
	if stripTypePrefix("Meeting: Palmetto Support") != "Palmetto Support" || stripTypePrefix("plain") != "plain" {
		t.Errorf("stripTypePrefix wrong")
	}
	if !isGenericChatName("") || !isGenericChatName(" Chat ") || isGenericChatName("Notes") {
		t.Errorf("isGenericChatName wrong")
	}
	if got := placeholderDMName("8:orgid:1ae9e827-5d2d-46a6-92db-e8c40211ef55"); got != "DM: unknown user 1ae9e827" {
		t.Errorf("placeholderDMName = %q", got)
	}
	if placeholderDMName("") != "" || !isPlaceholderDMName("DM: unknown user 1ae9e827") || isPlaceholderDMName("DM: Alex") {
		t.Errorf("placeholder helpers wrong")
	}
	for id, want := range map[string]bool{
		"19:28c8b4261c3541dca2e3533b3b7fdd25@thread.v2":                true,
		"19:meeting_NjAwOGMy@thread.v2":                                false,
		"19:teamsstream_notes_uid@thread.v2":                           false,
		"19:EaySVwXE_cGO3nrsIF3ZFl6fhuPGBZKrJDfrY_DC-QM1@thread.tacv2": false,
		"19:a_b@unq.gbl.spaces":                                        false,
	} {
		if got := isGroupChatThread(id); got != want {
			t.Errorf("isGroupChatThread(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestDMCounterpartFromThreadID(t *testing.T) {
	c := &TeamsClient{
		Meta:  &teamsid.UserLoginMetadata{TeamsUserID: "8:orgid:c61ce4d2-56e3-47d9-9e26-96fbff37fb62"},
		Login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "8:orgid:c61ce4d2-56e3-47d9-9e26-96fbff37fb62"}},
	}
	cases := map[string]string{
		"19:1ae9e827-5d2d-46a6-92db-e8c40211ef55_c61ce4d2-56e3-47d9-9e26-96fbff37fb62@unq.gbl.spaces": "8:orgid:1ae9e827-5d2d-46a6-92db-e8c40211ef55",
		"19:c61ce4d2-56e3-47d9-9e26-96fbff37fb62_d5d5805c-3c78-43b4-b48d-59595a3eafe3@unq.gbl.spaces": "8:orgid:d5d5805c-3c78-43b4-b48d-59595a3eafe3",
		"19:28c8b4261c3541dca2e3533b3b7fdd25@thread.v2":                                               "",
	}
	for id, want := range cases {
		if got := c.dmCounterpartFromThreadID(id); got != want {
			t.Errorf("dmCounterpartFromThreadID(%q) = %q, want %q", id, got, want)
		}
	}
	members := c.dmMemberListFromThreadID("19:1ae9e827-5d2d-46a6-92db-e8c40211ef55_c61ce4d2-56e3-47d9-9e26-96fbff37fb62@unq.gbl.spaces")
	if members == nil || len(members.MemberMap) != 2 {
		t.Fatalf("expected two DM members, got %#v", members)
	}
}
