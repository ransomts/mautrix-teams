package connector

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

// insertTestMessage stores a bridged message part in the test thread's
// portal, recording editTime in its metadata.  Own messages are sent as
// the login's Matrix user (double puppeted), others by a ghost.
func insertTestMessage(t *testing.T, c *TeamsClient, messageID, senderID string, editTime int64) {
	t.Helper()
	senderMXID := id.UserID("@ghost:test")
	if senderID == testSelfUserID {
		senderMXID = c.Login.UserMXID
	}
	key := c.portalKey(testThreadID)
	if p, _ := c.Main.Bridge.DB.Portal.GetByKey(context.Background(), key); p == nil {
		insertTestPortal(t, c, &database.Portal{PortalKey: key, MXID: "!room:test"})
	}
	if g, _ := c.Main.Bridge.DB.Ghost.GetByID(context.Background(), networkid.UserID(senderID)); g == nil {
		insertTestGhost(t, c, senderID, "")
	}
	msg := &database.Message{
		BridgeID:   schedTestBridgeID,
		ID:         networkid.MessageID(messageID),
		MXID:       id.EventID("$evt" + messageID),
		Room:       key,
		SenderID:   networkid.UserID(senderID),
		SenderMXID: senderMXID,
		Timestamp:  time.UnixMilli(1790788892511),
		Metadata:   &teamsid.MessageMetadata{EditTime: editTime},
	}
	if err := c.Main.Bridge.DB.Message.Insert(context.Background(), msg); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func editedRemoteMessage(id, sender string, editTime int64) model.RemoteMessage {
	props, _ := json.Marshal(map[string]any{"edittime": editTime})
	return model.RemoteMessage{
		MessageID:     id,
		SequenceID:    "19",
		SenderID:      sender,
		Body:          "edited text",
		MessageType:   "RichText/Html",
		PropertiesRaw: props,
	}
}

func TestQueueInPlaceEdit(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, nil, sink)
	ctx := context.Background()
	th := newTestThreadState()
	const editTime = int64(1790795629563)

	insertTestMessage(t, c, "1790788892511", testSelfUserID, 0)
	msg := editedRemoteMessage("1790788892511", testSelfUserID, editTime)

	c.queueInPlaceEdit(ctx, th, msg, "1790788892511", testSelfUserID)
	evts := sink.getEvents()
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1 edit", len(evts))
	}
	edit, ok := evts[0].(*simplevent.Message[model.RemoteMessage])
	if !ok || edit.GetType() != bridgev2.RemoteEventEdit {
		t.Fatalf("got %T %v, want an edit", evts[0], evts[0].GetType())
	}
	if edit.TargetMessage != "1790788892511" {
		t.Errorf("target = %q", edit.TargetMessage)
	}
	if !edit.Sender.IsFromMe {
		t.Error("own edit not marked as from me")
	}
	if !edit.Timestamp.Equal(time.UnixMilli(editTime)) {
		t.Errorf("timestamp = %v, want the edit time", edit.Timestamp)
	}

	// The next poll carries the same message again: nothing new.
	c.queueInPlaceEdit(ctx, th, msg, "1790788892511", testSelfUserID)
	if n := len(sink.getEvents()); n != 1 {
		t.Fatalf("same edit queued again: %d events", n)
	}

	// A later edit is queued.
	c.queueInPlaceEdit(ctx, th, editedRemoteMessage("1790788892511", testSelfUserID, editTime+1000), "1790788892511", testSelfUserID)
	if n := len(sink.getEvents()); n != 2 {
		t.Fatalf("later edit: %d events, want 2", n)
	}
}

func TestQueueInPlaceEditAlreadyBridged(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, nil, sink)
	ctx := context.Background()
	th := newTestThreadState()
	const editTime = int64(1790795629563)

	// Bridged with this edit already (e.g. before a restart emptied the
	// in-memory map).
	insertTestMessage(t, c, "100", testSelfUserID, editTime)
	c.queueInPlaceEdit(ctx, th, editedRemoteMessage("100", testSelfUserID, editTime), "100", testSelfUserID)
	// Never bridged.
	c.queueInPlaceEdit(ctx, th, editedRemoteMessage("200", testSelfUserID, editTime), "200", testSelfUserID)
	// Not edited.
	insertTestMessage(t, c, "300", testSelfUserID, 0)
	c.queueInPlaceEdit(ctx, th, model.RemoteMessage{MessageID: "300", SenderID: testSelfUserID}, "300", testSelfUserID)

	if evts := sink.getEvents(); len(evts) != 0 {
		t.Fatalf("got %d events, want none", len(evts))
	}
}

func TestConvertTeamsMessageRecordsEditTime(t *testing.T) {
	c := newTestClient(nil, nil)
	cm, err := c.convertTeamsMessage(context.Background(), nil, nil, editedRemoteMessage("1", "8:orgid:x", 42))
	if err != nil || cm == nil || len(cm.Parts) == 0 {
		t.Fatalf("convert: %v %v", cm, err)
	}
	meta, ok := cm.Parts[0].DBMetadata.(*teamsid.MessageMetadata)
	if !ok || meta.EditTime != 42 {
		t.Fatalf("part metadata = %#v, want EditTime 42", cm.Parts[0].DBMetadata)
	}
}

func TestConvertTeamsEditTargetsCaption(t *testing.T) {
	c := newTestClient(nil, nil)
	image := &database.Message{ID: "1", PartID: ""}
	caption := &database.Message{ID: "1", PartID: "caption"}
	ce, err := c.convertTeamsEdit(context.Background(), nil, nil, []*database.Message{image, caption}, editedRemoteMessage("1", "8:orgid:x", 42))
	if err != nil {
		t.Fatal(err)
	}
	if ce.ModifiedParts[0].Part != caption {
		t.Fatal("edit did not target the caption part")
	}
	if meta, ok := caption.Metadata.(*teamsid.MessageMetadata); !ok || meta.EditTime != 42 {
		t.Fatalf("caption metadata = %#v, want EditTime 42", caption.Metadata)
	}
}

func TestQueueInPlaceEditOwnMessageSentByGhost(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, nil, sink)
	// An own message bridged before double puppeting: sent by the ghost.
	insertTestMessage(t, c, "500", "8:orgid:other", 0)
	msg := editedRemoteMessage("500", testSelfUserID, 42)
	c.queueInPlaceEdit(context.Background(), newTestThreadState(), msg, "500", testSelfUserID)
	evts := sink.getEvents()
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if edit := evts[0].(*simplevent.Message[model.RemoteMessage]); edit.Sender.IsFromMe {
		t.Fatal("edit of a ghost-sent message marked as from me; bridgev2 would refuse it")
	}
}

func TestQueueInPlaceDelete(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, nil, sink)
	ctx := context.Background()
	th := newTestThreadState()

	insertTestMessage(t, c, "1790883490387", "8:orgid:other", 0)
	deleted := model.RemoteMessage{
		MessageID:     "1790883490387",
		SenderID:      "8:orgid:other",
		MessageType:   "RichText/Html",
		PropertiesRaw: []byte(`{"deletetime":1790883503427}`),
	}

	c.queueInPlaceChange(ctx, th, deleted, "1790883490387", testSelfUserID)
	evts := sink.getEvents()
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1 delete", len(evts))
	}
	remove, ok := evts[0].(*simplevent.MessageRemove)
	if !ok || remove.GetType() != bridgev2.RemoteEventMessageRemove || remove.TargetMessage != "1790883490387" {
		t.Fatalf("got %T %+v, want a removal of the message", evts[0], evts[0])
	}
	if remove.Sender.Sender != "" || remove.Sender.IsFromMe {
		t.Errorf("sender = %+v, want none (bridgev2 then redacts as the original sender)", remove.Sender)
	}

	// Later polls carry the same deleted message: nothing more.
	c.queueInPlaceChange(ctx, th, deleted, "1790883490387", testSelfUserID)
	// A deleted message that was never bridged: nothing to remove.
	never := deleted
	never.MessageID = "999"
	c.queueInPlaceChange(ctx, th, never, "999", testSelfUserID)
	if n := len(sink.getEvents()); n != 1 {
		t.Fatalf("got %d events, want still 1", n)
	}
}
