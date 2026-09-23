package connector

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// handleTypingControl acts only on typing controls newer than the cursor,
// recent, and from another user; ClearTyping stops the indicator.
func TestHandleTypingControl(t *testing.T) {
	now := time.Now()
	alice := "8:orgid:alice-uuid"
	cases := []struct {
		name        string
		msg         model.RemoteMessage
		lastSeq     string
		wantEvent   bool
		wantTimeout time.Duration
	}{
		{"typing", model.RemoteMessage{SequenceID: "101", SenderID: alice, MessageType: "Control/Typing", Timestamp: now}, "100", true, typingTimeout},
		{"clear typing", model.RemoteMessage{SequenceID: "101", SenderID: alice, MessageType: "Control/ClearTyping", Timestamp: now}, "100", true, 0},
		{"no cursor yet", model.RemoteMessage{SequenceID: "5", SenderID: alice, MessageType: "Control/Typing"}, "", true, typingTimeout},
		{"not newer than cursor", model.RemoteMessage{SequenceID: "100", SenderID: alice, MessageType: "Control/Typing", Timestamp: now}, "100", false, 0},
		{"stale", model.RemoteMessage{SequenceID: "101", SenderID: alice, MessageType: "Control/Typing", Timestamp: now.Add(-typingMaxAge - time.Second)}, "100", false, 0},
		{"self", model.RemoteMessage{SequenceID: "101", SenderID: testSelfUserID, MessageType: "Control/Typing", Timestamp: now}, "100", false, 0},
		{"no sender", model.RemoteMessage{SequenceID: "101", MessageType: "Control/Typing", Timestamp: now}, "100", false, 0},
		{"thread sender", model.RemoteMessage{SequenceID: "101", SenderID: testThreadID, MessageType: "Control/Typing", Timestamp: now}, "100", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &capturingEventSink{}
			c := newTestClient(&mockTeamsAPI{}, sink)
			c.handleTypingControl(newTestThreadState(), tc.msg, tc.lastSeq, model.NormalizeTeamsUserID(testSelfUserID), now)
			events := sink.getEvents()
			if !tc.wantEvent {
				if len(events) != 0 {
					t.Fatalf("expected no event, got %d", len(events))
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			typing, ok := events[0].(*simplevent.Typing)
			if !ok {
				t.Fatalf("expected *simplevent.Typing, got %T", events[0])
			}
			if typing.Timeout != tc.wantTimeout {
				t.Fatalf("timeout = %v, want %v", typing.Timeout, tc.wantTimeout)
			}
			if typing.PortalKey != c.portalKey(testThreadID) {
				t.Fatalf("unexpected portal key %v", typing.PortalKey)
			}
		})
	}
}

// A second typing control from the same sender inside the dedup window is
// dropped.
func TestHandleTypingControlDedups(t *testing.T) {
	now := time.Now()
	sink := &capturingEventSink{}
	c := newTestClient(&mockTeamsAPI{}, sink)
	msg := model.RemoteMessage{SequenceID: "101", SenderID: "8:orgid:alice-uuid", MessageType: "Control/Typing", Timestamp: now}
	c.handleTypingControl(newTestThreadState(), msg, "100", testSelfUserID, now)
	msg.SequenceID = "102"
	c.handleTypingControl(newTestThreadState(), msg, "100", testSelfUserID, now)
	if n := len(sink.getEvents()); n != 1 {
		t.Fatalf("expected 1 event after dedup, got %d", n)
	}
}
