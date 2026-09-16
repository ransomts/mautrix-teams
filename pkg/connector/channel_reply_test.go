package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestIsChannelThread(t *testing.T) {
	if !isChannelThread("19:abc@thread.tacv2") {
		t.Error("tacv2 is a channel thread")
	}
	for _, id := range []string{"19:a_b@unq.gbl.spaces", "19:x@thread.v2", "19:teamsstream_notes_x@thread.v2"} {
		if isChannelThread(id) {
			t.Errorf("%s is not a channel thread", id)
		}
	}
}

func TestResolveTeamsThreadRoot(t *testing.T) {
	// Matrix thread root wins.
	msg := &bridgev2.MatrixMessage{
		ThreadRoot: &database.Message{ID: networkid.MessageID("root-1")},
		ReplyTo:    &database.Message{ID: networkid.MessageID("mid-9"), ThreadRoot: networkid.MessageID("root-9")},
	}
	if got := resolveTeamsThreadRoot(msg); got != "root-1" {
		t.Fatalf("thread root should win: %q", got)
	}
	// Else the replied-to message's stored thread root.
	msg = &bridgev2.MatrixMessage{ReplyTo: &database.Message{ID: "mid-9", ThreadRoot: "root-9"}}
	if got := resolveTeamsThreadRoot(msg); got != "root-9" {
		t.Fatalf("reply thread root: %q", got)
	}
	// Else the replied-to message itself (it is the root).
	msg = &bridgev2.MatrixMessage{ReplyTo: &database.Message{ID: "only"}}
	if got := resolveTeamsThreadRoot(msg); got != "only" {
		t.Fatalf("reply id fallback: %q", got)
	}
	// No reply target.
	if got := resolveTeamsThreadRoot(&bridgev2.MatrixMessage{}); got != "" {
		t.Fatalf("empty expected: %q", got)
	}
}
