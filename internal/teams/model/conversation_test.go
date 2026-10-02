package model

import (
	"encoding/json"
	"testing"
)

func TestNormalizeMissingID(t *testing.T) {
	conv := RemoteConversation{
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "",
			ProductThreadType: "OneToOneChat",
		},
	}
	_, ok := conv.Normalize()
	if ok {
		t.Fatalf("expected Normalize to skip when originalThreadId is missing")
	}
}

func TestNormalizeOneToOne(t *testing.T) {
	conv := RemoteConversation{
		ID: "@oneToOne.skype",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-123",
			ProductThreadType: "OneToOneChat",
			CreatedAt:         "2024-01-01T00:00:00Z",
			IsCreator:         true,
		},
		Members: []ConversationMember{
			{ID: "8:self", DisplayName: "Me", IsSelf: true},
			{ID: "8:other", DisplayName: "Alex"},
		},
	}
	thread, ok := conv.NormalizeForSelf("8:self")
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if thread.ID != "thread-123" {
		t.Fatalf("unexpected thread ID: %q", thread.ID)
	}
	if thread.ConversationID != "@oneToOne.skype" {
		t.Fatalf("unexpected conversation ID: %q", thread.ConversationID)
	}
	if !thread.IsOneToOne {
		t.Fatalf("expected IsOneToOne to be true")
	}
	if thread.CreatedAtRaw != "2024-01-01T00:00:00Z" {
		t.Fatalf("unexpected created_at: %q", thread.CreatedAtRaw)
	}
	if !thread.IsCreator {
		t.Fatalf("expected IsCreator to be true")
	}
	if thread.RoomName != "Alex" {
		t.Fatalf("unexpected room name: %q", thread.RoomName)
	}
}

func TestNormalizeGroupUsesThreadName(t *testing.T) {
	conv := RemoteConversation{
		ID: "19:group@thread.v2",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-group",
			ProductThreadType: "GroupChat",
			Topic:             "Project Alpha",
		},
	}
	thread, ok := conv.NormalizeForSelf("8:self")
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if thread.RoomName != "Project Alpha" {
		t.Fatalf("unexpected room name: %q", thread.RoomName)
	}
}

func TestNormalizeFallbackRoomName(t *testing.T) {
	conv := RemoteConversation{
		ID: "19:group@thread.v2",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-group",
			ProductThreadType: "GroupChat",
		},
	}
	thread, ok := conv.Normalize()
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if thread.RoomName != "Chat" {
		t.Fatalf("unexpected fallback room name: %q", thread.RoomName)
	}
}

func TestNormalizeBotAugmentedOneToOneStillUsesHumanName(t *testing.T) {
	conv := RemoteConversation{
		ID: "19:thread@thread.v2",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-bot-mixed",
			ProductThreadType: "GroupChat",
		},
		Members: []ConversationMember{
			{ID: "8:self", DisplayName: "Me", IsSelf: true},
			{ID: "28:teamsbot", DisplayName: "Teams Bot"},
			{ID: "8:live:.cid.remote", DisplayName: "Max W"},
		},
	}
	thread, ok := conv.NormalizeForSelf("8:self")
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if !thread.IsOneToOne {
		t.Fatalf("expected bot-augmented chat to be treated as one-to-one")
	}
	if thread.RoomName != "Max W" {
		t.Fatalf("unexpected room name: %q", thread.RoomName)
	}
}

func TestNormalizeOneToOneSkipsSelfGhostDisplayName(t *testing.T) {
	conv := RemoteConversation{
		ID: "@oneToOne.skype",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-ghost-dm",
			ProductThreadType: "OneToOneChat",
		},
		Members: []ConversationMember{
			{ID: "8:live:self", DisplayName: "Me", IsSelf: true},
			{ID: "28:teamsbot", DisplayName: "Teams Bot"},
			{ID: "29:self-ghost", DisplayName: "Me"},
			{ID: "29:remote-ghost", DisplayName: "Alex"},
		},
	}

	thread, ok := conv.NormalizeForSelf("8:live:self")
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if !thread.IsOneToOne {
		t.Fatalf("expected IsOneToOne to be true")
	}
	if thread.RoomName != "Alex" {
		t.Fatalf("unexpected room name: %q", thread.RoomName)
	}
}

func TestNormalizeGroupChatInferenceHandlesSelfGhostPair(t *testing.T) {
	conv := RemoteConversation{
		ID: "19:thread@thread.v2",
		ThreadProperties: ThreadProperties{
			OriginalThreadID:  "thread-ghost-inferred",
			ProductThreadType: "GroupChat",
		},
		Members: []ConversationMember{
			{ID: "8:live:self", DisplayName: "Me", IsSelf: true},
			{ID: "28:teamsbot", DisplayName: "Teams Bot"},
			{ID: "29:self-ghost", DisplayName: "Me"},
			{ID: "29:remote-ghost", DisplayName: "Alex"},
		},
	}

	thread, ok := conv.NormalizeForSelf("8:live:self")
	if !ok {
		t.Fatalf("expected Normalize to succeed")
	}
	if !thread.IsOneToOne {
		t.Fatalf("expected bot+ghost augmented chat to be treated as one-to-one")
	}
	if thread.RoomName != "Alex" {
		t.Fatalf("unexpected room name: %q", thread.RoomName)
	}
}

func TestConversationUserState(t *testing.T) {
	// The Boyer DM before and after "Mark as unread" and "Mute" (2026-10-02).
	var conv RemoteConversation
	raw := `{"id":"19:a_b@unq.gbl.spaces","properties":{"consumptionhorizon":"1790788892511;1790788892662;1790788892467","consumptionHorizonBookmark":"1790788892510;1790947736042;1790788892467","alerts":"false","isemptyconversation":"False"}}`
	if err := json.Unmarshal([]byte(raw), &conv); err != nil {
		t.Fatal(err)
	}
	if !conv.Properties.Muted() || !conv.Properties.MarkedUnread() {
		t.Errorf("muted %v unread %v, want both", conv.Properties.Muted(), conv.Properties.MarkedUnread())
	}

	cases := []struct {
		props       ConversationProperties
		muted, unrd bool
	}{
		{ConversationProperties{}, false, false},
		{ConversationProperties{Alerts: "true"}, false, false},
		// Read again after the mark: the read position is later.
		{ConversationProperties{ConsumptionHorizon: "5;300;x", ConsumptionHorizonBookmark: "4;200;x"}, false, false},
		{ConversationProperties{ConsumptionHorizonBookmark: ""}, false, false},
		{ConversationProperties{ConsumptionHorizonBookmark: "4;200;x"}, false, true},
	}
	for i, tc := range cases {
		if tc.props.Muted() != tc.muted || tc.props.MarkedUnread() != tc.unrd {
			t.Errorf("case %d: muted %v unread %v", i, tc.props.Muted(), tc.props.MarkedUnread())
		}
	}

	// An oddly typed property does not fail the list.
	if err := json.Unmarshal([]byte(`{"properties":{"alerts":false,"consumptionhorizon":{"x":1}}}`), &conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !conv.Properties.Muted() || conv.Properties.ConsumptionHorizon != "" {
		t.Errorf("got %+v", conv.Properties)
	}
}
