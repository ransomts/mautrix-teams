package connector

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

// pinsAPI is mockTeamsAPI with the pinned-chats calls.
type pinsAPI struct {
	mockTeamsAPI
	mu     sync.Mutex
	pins   map[string]int
	pinErr error
	sets   []pinSet
}

type pinSet struct {
	conversationID string
	pinned         bool
}

func (a *pinsAPI) GetPinnedChats(context.Context) (map[string]int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pinErr != nil {
		return nil, a.pinErr
	}
	out := make(map[string]int, len(a.pins))
	for k, v := range a.pins {
		out[k] = v
	}
	return out, nil
}

func (a *pinsAPI) SetChatPinned(_ context.Context, conversationID string, pinned bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sets = append(a.sets, pinSet{conversationID, pinned})
	return nil
}

func (a *pinsAPI) setPins(pins map[string]int, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pins, a.pinErr = pins, err
}

func (a *pinsAPI) getSets() []pinSet {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]pinSet(nil), a.sets...)
}

func tagOf(info *bridgev2.ChatInfo) string {
	if info == nil || info.UserLocal == nil || info.UserLocal.Tag == nil {
		return "<nil>"
	}
	return string(*info.UserLocal.Tag)
}

func TestPinTrackerObserve(t *testing.T) {
	var p stateTracker
	steps := []struct {
		pinned bool
		want   string
	}{
		{false, "<nil>"},      // first sight, not pinned: say nothing
		{true, "m.favourite"}, // pinned
		{true, "<nil>"},       // still pinned
		{false, ""},           // unpinned
		{false, "<nil>"},      // still unpinned
		{true, "m.favourite"}, // pinned again
	}
	for i, s := range steps {
		got := "<nil>"
		if tag := p.observe("t", s.pinned); tag != nil {
			got = string(*tag)
		}
		if got != s.want {
			t.Errorf("step %d (pinned=%v): tag %q, want %q", i, s.pinned, got, s.want)
		}
	}
	var fresh stateTracker
	if tag := fresh.observe("t", true); tag == nil || *tag != event.RoomTagFavourite {
		t.Errorf("first sight of a pin: %v", tag)
	}
}

func TestRefreshThreadsBridgesPins(t *testing.T) {
	notes := conv(discNotes, "StreamOfNotes", "")
	notes.ID = "48:notes" // the pinned list keys the notes chat by this
	api := &pinsAPI{
		mockTeamsAPI: mockTeamsAPI{conversations: []model.RemoteConversation{
			notes,
			conv(outDMThread, "OneToOneChat", ""),
			conv(outGroupThread, "Chat", "Project X"),
		}},
		pins: map[string]int{"48:notes": 1, outDMThread: 2},
	}
	c, sink, _ := newDiscoveryClient(t, api)
	ctx := context.Background()

	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	events := resyncs(t, sink)
	if len(events) != 3 {
		t.Fatalf("first pass resyncs %v", keys(events))
	}
	for id, want := range map[string]string{discNotes: "m.favourite", outDMThread: "m.favourite", outGroupThread: "<nil>"} {
		if got := tagOf(events[networkid.PortalID(id)].ChatInfo); got != want {
			t.Errorf("first pass %s: tag %q, want %q", id, got, want)
		}
	}

	// Nothing changed: nothing announced.
	sink.reset()
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("unchanged pass: %d events", n)
	}

	// The DM is unpinned and the group pinned: only those two re-announced.
	sink.reset()
	api.setPins(map[string]int{"48:notes": 1, outGroupThread: 2}, nil)
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	events = resyncs(t, sink)
	if len(events) != 2 {
		t.Fatalf("pin change resyncs %v", keys(events))
	}
	if got := tagOf(events[outDMThread].ChatInfo); got != "" {
		t.Errorf("unpinned DM tag %q", got)
	}
	if got := tagOf(events[outGroupThread].ChatInfo); got != "m.favourite" {
		t.Errorf("pinned group tag %q", got)
	}

	// The pinned list cannot be read: the last known state stands, so
	// nothing is announced and no pin is cleared.
	sink.reset()
	api.setPins(nil, errors.New("properties unavailable"))
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("failed pin read announced %d events", n)
	}
	if pinned, known := c.pins.get(outGroupThread); !known || !pinned {
		t.Error("failed pin read forgot the group's pin")
	}
}

func TestRefreshThreadsWithoutPinsAPI(t *testing.T) {
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{conv(outGroupThread, "Chat", "Project X")}}
	c, sink, _ := newDiscoveryClient(t, api)
	if err := c.refreshThreads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tagOf(resyncs(t, sink)[outGroupThread].ChatInfo); got != "<nil>" {
		t.Errorf("tag %q without a pins API", got)
	}
}

func roomTagMsg(threadID string, now, prev event.Tags) *bridgev2.MatrixRoomTag {
	msg := &bridgev2.MatrixRoomTag{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.TagEventContent]{
			Content: &event.TagEventContent{Tags: now},
			Portal:  &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: networkid.PortalID(threadID)}}},
		},
	}
	if prev != nil {
		msg.PrevContent = &event.TagEventContent{Tags: prev}
	}
	return msg
}

func TestHandleRoomTag(t *testing.T) {
	api := &pinsAPI{}
	c, _, _ := newDiscoveryClient(t, api)
	ctx := context.Background()
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: discNotes, Conversation: "48:notes", Name: "Notes"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outGroupThread, Conversation: outGroupThread, Name: "Project X"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: chanGeneral, Conversation: chanGeneral, Name: "General"})
	fav := event.Tags{event.RoomTagFavourite: {}}
	low := event.Tags{event.RoomTagLowPriority: {}}

	// Favourited on Matrix: pinned on Teams, by conversation ID.
	if err := c.HandleRoomTag(ctx, roomTagMsg(discNotes, fav, event.Tags{})); err != nil {
		t.Fatal(err)
	}
	// The same again (the pin is now known): no second write.
	if err := c.HandleRoomTag(ctx, roomTagMsg(discNotes, fav, nil)); err != nil {
		t.Fatal(err)
	}
	// Another tag changed while favourite stayed: nothing to do.
	both := event.Tags{event.RoomTagFavourite: {}, "u.work": {}}
	if err := c.HandleRoomTag(ctx, roomTagMsg(outGroupThread, both, fav)); err != nil {
		t.Fatal(err)
	}
	// Unfavourited: unpinned.
	if err := c.HandleRoomTag(ctx, roomTagMsg(discNotes, low, fav)); err != nil {
		t.Fatal(err)
	}
	// Channels and portals without thread state (team spaces) are skipped.
	if err := c.HandleRoomTag(ctx, roomTagMsg(chanGeneral, fav, event.Tags{})); err != nil {
		t.Fatal(err)
	}
	if err := c.HandleRoomTag(ctx, roomTagMsg("team:abc", fav, event.Tags{})); err != nil {
		t.Fatal(err)
	}

	want := []pinSet{{"48:notes", true}, {"48:notes", false}}
	got := api.getSets()
	if len(got) != len(want) {
		t.Fatalf("pin writes %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("write %d: %v, want %v", i, got[i], want[i])
		}
	}
}

func TestHandleRoomTagEchoOfTeamsPin(t *testing.T) {
	api := &pinsAPI{}
	c, _, _ := newDiscoveryClient(t, api)
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outGroupThread, Conversation: outGroupThread, Name: "Project X"})
	// Discovery saw the pin on Teams and bridgev2 tagged the room; the tag
	// coming back is not written to Teams again.
	c.pins.observe(outGroupThread, true)
	if err := c.HandleRoomTag(context.Background(), roomTagMsg(outGroupThread, event.Tags{event.RoomTagFavourite: {}}, event.Tags{})); err != nil {
		t.Fatal(err)
	}
	if sets := api.getSets(); len(sets) != 0 {
		t.Fatalf("echo written to Teams: %v", sets)
	}
}

func TestHandleRoomTagErrors(t *testing.T) {
	c, _, _ := newDiscoveryClient(t, &mockTeamsAPI{})
	ctx := context.Background()
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outGroupThread, Conversation: outGroupThread, Name: "Project X"})
	fav := event.Tags{event.RoomTagFavourite: {}}
	if err := c.HandleRoomTag(ctx, nil); err == nil {
		t.Error("nil message accepted")
	}
	// A client without the pinned-chats calls cannot pin.
	if err := c.HandleRoomTag(ctx, roomTagMsg(outGroupThread, fav, event.Tags{})); err == nil {
		t.Error("pin without a pins API succeeded")
	}
	c.loggedIn.Store(false)
	c.Meta.SkypeToken = ""
	if err := c.HandleRoomTag(ctx, roomTagMsg(outGroupThread, fav, event.Tags{})); !errors.Is(err, bridgev2.ErrNotLoggedIn) {
		t.Errorf("logged out: %v", err)
	}
}

// splitChatState separates a pass's chat resyncs from its mark-unread events.
func splitChatState(t *testing.T, sink *capturingEventSink) (map[networkid.PortalID]*simplevent.ChatResync, map[networkid.PortalID]bool) {
	t.Helper()
	rs := make(map[networkid.PortalID]*simplevent.ChatResync)
	unread := make(map[networkid.PortalID]bool)
	for _, evt := range sink.getEvents() {
		switch e := evt.(type) {
		case *simplevent.ChatResync:
			rs[e.PortalKey.ID] = e
		case *simplevent.MarkUnread:
			if !e.Sender.IsFromMe {
				t.Errorf("mark unread for %s not from me", e.PortalKey.ID)
			}
			unread[e.PortalKey.ID] = e.Unread
		default:
			t.Fatalf("unexpected event %T", evt)
		}
	}
	return rs, unread
}

func mutedUntilOf(info *bridgev2.ChatInfo) *time.Time {
	if info == nil || info.UserLocal == nil {
		return nil
	}
	return info.UserLocal.MutedUntil
}

func TestRefreshThreadsBridgesMuteAndUnread(t *testing.T) {
	dm := conv(outDMThread, "OneToOneChat", "")
	group := conv(outGroupThread, "Chat", "Project X")
	// As the Boyer DM was after "Mute" and "Mark as unread" (2026-10-02).
	dm.Properties.Alerts = "false"
	dm.Properties.ConsumptionHorizon = "1790788892511;1790788892662;1790788892467"
	dm.Properties.ConsumptionHorizonBookmark = "1790788892510;1790947736042;1790788892467"
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{dm, group}}
	c, sink, _ := newDiscoveryClient(t, api)
	ctx := context.Background()

	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	rs, unread := splitChatState(t, sink)
	if m := mutedUntilOf(rs[outDMThread].ChatInfo); m == nil || !m.Equal(event.MutedForever) {
		t.Errorf("DM muted until %v, want forever", m)
	}
	// Not muted, seen for the first time: nothing said, so a mute set on
	// Matrix stays.
	if m := mutedUntilOf(rs[outGroupThread].ChatInfo); m != nil {
		t.Errorf("group muted until %v, want nothing", m)
	}
	if got, ok := unread[outDMThread]; !ok || !got {
		t.Errorf("DM unread events %v, want marked unread", unread)
	}
	if _, ok := unread[outGroupThread]; ok {
		t.Errorf("group got a mark-unread event: %v", unread)
	}

	// Nothing changed: nothing announced.
	sink.reset()
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("unchanged pass: %d events", n)
	}

	// The DM is unmuted and read again (the read position passes the mark).
	sink.reset()
	api.conversations[0].Properties.Alerts = "true"
	api.conversations[0].Properties.ConsumptionHorizon = "1790788892511;1790950000000;1790788892467"
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	rs, unread = splitChatState(t, sink)
	if len(rs) != 1 {
		t.Fatalf("unmute resyncs %v", keys(rs))
	}
	if m := mutedUntilOf(rs[outDMThread].ChatInfo); m == nil || m.After(time.Now()) {
		t.Errorf("DM muted until %v, want unmuted", m)
	}
	if got, ok := unread[outDMThread]; !ok || got {
		t.Errorf("DM unread events %v, want read again", unread)
	}
}
