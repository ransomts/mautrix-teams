package connector

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

// The account under test and two colleagues, as Teams MRIs.  discSelf is
// the first member of outDMThread, discBob the second.
const (
	discSelf = "8:orgid:00000001-0000-0000-0000-000000000001"
	discBob  = "8:orgid:00000002-0000-0000-0000-000000000002"
	discAnn  = "8:orgid:00000003-0000-0000-0000-000000000003"

	discUnnamedGroup = "19:1fedcba9876543210fedcba987654321@thread.v2"
	discMeeting      = "19:meeting_NjAwOGMyYzAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAw@thread.v2"
	discDrafts       = "19:teamsstream_drafts_00000001-0000-0000-0000-000000000001@thread.v2"
	discNotes        = "19:teamsstream_notes_00000001-0000-0000-0000-000000000001@thread.v2"
	discCallLogs     = "19:teamsstream_calllogs_00000001-0000-0000-0000-000000000001@thread.v2"
)

// listErrAPI fails ListConversations.
type listErrAPI struct {
	mockTeamsAPI
	err error
}

func (a *listErrAPI) ListConversations(context.Context, string) ([]model.RemoteConversation, error) {
	return nil, a.err
}

// conv builds a conversation-list entry the way the chat service lists
// them: the thread in threadProperties.originalThreadId (the same as id
// for enterprise chats), the kind in productThreadType, the chat's title
// in threadProperties.topic, and no member data.
func conv(threadID, productType, topic string) model.RemoteConversation {
	return model.RemoteConversation{
		ID: threadID,
		ThreadProperties: model.ThreadProperties{
			OriginalThreadID:  threadID,
			ProductThreadType: productType,
			Topic:             topic,
			CreatedAt:         "1726000000000",
		},
	}
}

func newDiscoveryClient(t *testing.T, api TeamsAPI) (*TeamsClient, *capturingEventSink, *fakeMatrix) {
	t.Helper()
	sink := &capturingEventSink{}
	c, mx := newBridgeTestClient(t, api, sink)
	c.Meta.TeamsUserID = discSelf
	return c, sink, mx
}

func threadRows(t *testing.T, c *TeamsClient) map[string]*teamsdb.ThreadState {
	t.Helper()
	rows, err := c.Main.DB.ThreadState.ListForLogin(context.Background(), c.Login.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*teamsdb.ThreadState, len(rows))
	for _, r := range rows {
		out[r.ThreadID] = r
	}
	return out
}

func memberIDs(m *bridgev2.ChatMemberList) []string {
	if m == nil {
		return nil
	}
	var out []string
	for id := range m.MemberMap {
		out = append(out, string(id))
	}
	slices.Sort(out)
	return out
}

func strp(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestRefreshThreadsDiscoversConversations(t *testing.T) {
	withMembers := conv("19:2fedcba9876543210fedcba987654321@thread.v2", "Chat", "Study group")
	withMembers.Members = []model.ConversationMember{
		{ID: discSelf}, {ID: discAnn}, {ID: "28:00000000-0000-0000-0000-00000000b075"},
	}
	noThreadID := conv("", "Chat", "Ghost chat")
	noThreadID.ID = "19:3fedcba9876543210fedcba987654321@thread.v2"
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{
		conv(outGroupThread, "Chat", "Project X"),
		conv(discUnnamedGroup, "Chat", ""),
		conv(chanGeneral, "TeamsStandardChannel", ""),
		conv(outDMThread, "OneToOneChat", ""),
		conv(discDrafts, "", ""),
		conv(discNotes, "", ""),
		withMembers,
		noThreadID,
	}}
	c, sink, _ := newDiscoveryClient(t, api)
	ctx := context.Background()
	// The channel was named on an earlier run, and has a poll cursor.
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: chanGeneral, Conversation: chanGeneral, Name: "CS Department / General", LastSequenceID: "77"})
	if err := c.Main.DB.Profile.Upsert(ctx, discBob, "Bob Example", time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := c.refreshThreads(ctx); err != nil {
		t.Fatalf("refreshThreads: %v", err)
	}

	rows := threadRows(t, c)
	wantNames := map[string]string{
		outGroupThread:   "Project X",
		discUnnamedGroup: "Chat",
		chanGeneral:      "CS Department / General", // the API's "Chat" does not replace it
		outDMThread:      "Bob Example",             // from the profile table via the thread ID
		discNotes:        "Chat",
		withMembers.ID:   "Study group",
	}
	if len(rows) != len(wantNames) {
		t.Errorf("%d thread rows, want %d", len(rows), len(wantNames))
	}
	for threadID, want := range wantNames {
		row := rows[threadID]
		if row == nil {
			t.Errorf("%s: no thread row", threadID)
			continue
		}
		if row.Name != want || row.Conversation != threadID || row.IsOneToOne != (threadID == outDMThread) {
			t.Errorf("%s: row %+v, want name %q", threadID, row, want)
		}
	}
	if rows[discDrafts] != nil {
		t.Error("drafts system stream got a thread row")
	}
	if rows[chanGeneral].LastSequenceID != "77" {
		t.Errorf("discovery reset the poll cursor: %q", rows[chanGeneral].LastSequenceID)
	}

	events := resyncs(t, sink)
	if len(events) != len(wantNames) {
		t.Fatalf("resyncs for %v", keys(events))
	}
	for id, rs := range events {
		if rs.PortalKey.Receiver != c.Login.ID || !rs.CreatePortal || !rs.ChatInfo.CanBackfill {
			t.Errorf("%s: event %+v", id, rs.EventMeta)
		}
	}
	info := func(id string) *bridgev2.ChatInfo { return events[networkid.PortalID(id)].ChatInfo }

	// A titled group chat: the title (threadProperties.topic, where Teams
	// keeps it) is the name; the topic is cleared, not a copy of the title.
	if got := info(outGroupThread); strp(got.Name) != "Project X" || got.Topic == nil || *got.Topic != "" ||
		*got.Type != database.RoomTypeDefault || got.Members != nil {
		t.Errorf("titled group: %+v", got)
	}
	// Generic names are never announced.
	for _, id := range []string{discUnnamedGroup, discNotes} {
		if got := info(id); got.Name != nil {
			t.Errorf("%s announced generic name %q", id, *got.Name)
		}
	}
	if got := info(chanGeneral); strp(got.Name) != "CS Department / General" {
		t.Errorf("channel name %q", strp(got.Name))
	}
	// The DM: named after the counterpart, typed DM, both members from the
	// thread ID even though the list had no member data.
	dm := info(outDMThread)
	if strp(dm.Name) != "Bob Example" || *dm.Type != database.RoomTypeDM {
		t.Errorf("dm: %+v", dm)
	}
	if got := memberIDs(dm.Members); !slices.Equal(got, []string{discSelf, discBob}) {
		t.Errorf("dm members %v", got)
	}
	// Member data, when present: bots dropped, self marked as self.
	gm := info(withMembers.ID).Members
	if got := memberIDs(gm); !slices.Equal(got, []string{discSelf, discAnn}) {
		t.Errorf("group members %v", got)
	}
	if self := gm.MemberMap[networkid.UserID(discSelf)]; !self.IsFromMe || self.SenderLogin != c.Login.ID || self.Membership != event.MembershipJoin {
		t.Errorf("self member %+v", self)
	}
}

func TestRefreshThreadsAnnouncesOnlyChanges(t *testing.T) {
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{
		conv(outGroupThread, "Chat", "Project X"),
		conv(discUnnamedGroup, "Chat", ""),
	}}
	c, sink, _ := newDiscoveryClient(t, api)
	ctx := context.Background()
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 2 {
		t.Fatalf("first pass: %d events", n)
	}

	sink.reset()
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("unchanged pass announced %d chats", n)
	}

	// The chat is renamed in Teams: announced, and stored.
	sink.reset()
	api.mu.Lock()
	api.conversations[0].ThreadProperties.Topic = "Project Y"
	api.mu.Unlock()
	if err := c.refreshThreads(ctx); err != nil {
		t.Fatal(err)
	}
	got := resyncs(t, sink)
	if len(got) != 1 || strp(got[outGroupThread].ChatInfo.Name) != "Project Y" {
		t.Fatalf("rename: %v", keys(got))
	}
	if row := threadRows(t, c)[outGroupThread]; row.Name != "Project Y" {
		t.Fatalf("stored name %q", row.Name)
	}
}

// A conversation that disappears from the list is left as it is: nothing
// is announced for it and its thread row stays (it is still polled).
func TestRefreshThreadsKeepsConversationsNoLongerListed(t *testing.T) {
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{conv(outGroupThread, "Chat", "Project X")}}
	c, sink, _ := newDiscoveryClient(t, api)
	if err := c.refreshThreads(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink.reset()
	api.mu.Lock()
	api.conversations = nil
	api.mu.Unlock()
	if err := c.refreshThreads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("%d events", n)
	}
	if threadRows(t, c)[outGroupThread] == nil {
		t.Fatal("thread row removed")
	}
}

func TestRefreshThreadsErrors(t *testing.T) {
	listErr := errors.New("conversations request failed")
	c, sink, _ := newDiscoveryClient(t, &listErrAPI{err: listErr})
	if err := c.refreshThreads(context.Background()); !errors.Is(err, listErr) {
		t.Fatalf("err %v", err)
	}

	// An expired token with nothing to refresh it with: nothing is listed.
	c.Meta.SkypeTokenExpiresAt = time.Now().Add(-time.Hour).Unix()
	c.Meta.RefreshToken = ""
	if err := c.refreshThreads(context.Background()); err == nil {
		t.Fatal("no error with an expired token")
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("%d events", n)
	}

	// No bridge: a no-op, not a panic.
	var nilClient *TeamsClient
	if err := nilClient.refreshThreads(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Naming passes
// ---------------------------------------------------------------------------

// insertSentMessage stores a bridged message from sender in threadID's portal.
func insertSentMessage(t *testing.T, c *TeamsClient, threadID, sender, msgID string) {
	t.Helper()
	ctx := context.Background()
	if g, _ := c.Main.Bridge.DB.Ghost.GetByID(ctx, networkid.UserID(sender)); g == nil {
		insertTestGhost(t, c, sender, "")
	}
	err := c.Main.Bridge.DB.Message.Insert(ctx, &database.Message{
		BridgeID:  schedTestBridgeID,
		ID:        networkid.MessageID(msgID),
		MXID:      id.EventID("$" + msgID),
		Room:      c.portalKey(threadID),
		SenderID:  networkid.UserID(sender),
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func TestResolveUnnamedGroupChatsFromSenders(t *testing.T) {
	c, sink, _ := newDiscoveryClient(t, &mockTeamsAPI{})
	ctx := context.Background()
	insertTestPortal(t, c, &database.Portal{PortalKey: c.portalKey(discUnnamedGroup), MXID: "!group:example.org"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: discUnnamedGroup, Conversation: discUnnamedGroup, Name: "Chat"})
	insertSentMessage(t, c, discUnnamedGroup, discBob, "1726000000001")
	insertSentMessage(t, c, discUnnamedGroup, discAnn, "1726000000002")
	insertSentMessage(t, c, discUnnamedGroup, discBob, "1726000000003")
	insertSentMessage(t, c, discUnnamedGroup, discSelf, "1726000000004")
	insertSentMessage(t, c, discUnnamedGroup, "8:orgid:00000009-0000-0000-0000-000000000009", "1726000000005") // no profile
	for id, name := range map[string]string{discBob: "Bob Example", discAnn: "Ann Example", discSelf: "Me Example"} {
		if err := c.Main.DB.Profile.Upsert(ctx, id, name, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Chats that are not unnamed group chats are left alone.
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outGroupThread, Conversation: outGroupThread, Name: "Project X"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: chanGeneral, Conversation: chanGeneral, Name: "Chat"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outDMThread, Conversation: outDMThread, IsOneToOne: true})

	c.resolveUnnamedGroupChats(ctx)

	got := resyncs(t, sink)
	if len(got) != 1 {
		t.Fatalf("resyncs %v", keys(got))
	}
	rs := got[discUnnamedGroup]
	if strp(rs.ChatInfo.Name) != "Ann Example, Bob Example" || rs.CreatePortal || rs.ChatInfo.Members != nil {
		t.Errorf("resync %+v %+v", rs.EventMeta, rs.ChatInfo)
	}
	if row := threadRows(t, c)[discUnnamedGroup]; row.Name != "Ann Example, Bob Example" {
		t.Errorf("stored %q", row.Name)
	}
}

func TestResolveUnnamedGroupChatsAsksForMembers(t *testing.T) {
	api := &mockTeamsAPI{threadMembers: map[string][]string{
		discUnnamedGroup: {discSelf, discAnn, discBob, "28:00000000-0000-0000-0000-00000000b075"},
	}}
	c, sink, _ := newDiscoveryClient(t, api)
	ctx := context.Background()
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: discUnnamedGroup, Conversation: discUnnamedGroup, Name: "Chat"})
	// Only Ann is known; Bob cannot be looked up (no Graph token).
	if err := c.Main.DB.Profile.Upsert(ctx, discAnn, "Ann Example", time.Now()); err != nil {
		t.Fatal(err)
	}

	c.resolveUnnamedGroupChats(ctx)

	rs := resyncs(t, sink)[discUnnamedGroup]
	if rs == nil || strp(rs.ChatInfo.Name) != "Ann Example" {
		t.Fatalf("resync %+v", rs)
	}
	// Everyone but the bot joins, Bob under his ID.
	if got := memberIDs(rs.ChatInfo.Members); !slices.Equal(got, []string{discSelf, discBob, discAnn}) {
		t.Errorf("members %v", got)
	}
	if !rs.ChatInfo.Members.MemberMap[networkid.UserID(discSelf)].IsFromMe {
		t.Error("self not marked")
	}
}

func TestJoinGroupNames(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"Bob", "Ann"}, "Ann, Bob"},
		{[]string{"Dee", "Cat", "Bob", "Ann"}, "Ann, Bob, Cat, Dee"},
		{[]string{"Eve", "Dee", "Cat", "Bob", "Ann"}, "Ann, Bob, Cat +2 others"},
	}
	for _, tc := range cases {
		in := slices.Clone(tc.in)
		if got := joinGroupNames(tc.in); got != tc.want {
			t.Errorf("joinGroupNames(%v) = %q, want %q", tc.in, got, tc.want)
		}
		if !slices.Equal(in, tc.in) {
			t.Errorf("joinGroupNames sorted its argument in place")
		}
	}
}

func TestApplyStructuredRoomNames(t *testing.T) {
	c, sink, _ := newDiscoveryClient(t, &mockTeamsAPI{})
	ctx := context.Background()
	unknownDM := "19:00000001-0000-0000-0000-000000000001_0000000a-0000-0000-0000-00000000000a@unq.gbl.spaces"
	for _, th := range []*teamsdb.ThreadState{
		{ThreadID: discCallLogs, Name: "Chat"},
		{ThreadID: discNotes, Name: "Notes"}, // already right
		{ThreadID: outDMThread, IsOneToOne: true, Name: "Bob Example"},
		{ThreadID: unknownDM, IsOneToOne: true},
		{ThreadID: chanGeneral, Name: "Chat"},
		{ThreadID: chanRandom, Name: "CS Department / Random"}, // already right
		{ThreadID: discMeeting, Name: "Standup"},
		{ThreadID: outGroupThread, Name: "Group: Project X"}, // already prefixed
		{ThreadID: discUnnamedGroup, Name: "Chat"},           // no name to prefix
	} {
		th.Conversation = th.ThreadID
		addTestThread(t, c, th)
	}
	channelMap := map[string]graph.ChannelInfo{
		chanGeneral: {TeamID: teamCS, TeamName: "CS Department", ChannelName: "General", Description: " Course announcements "},
		chanRandom:  {TeamID: teamCS, TeamName: "CS Department", ChannelName: "Random"},
	}

	c.applyStructuredRoomNames(ctx, channelMap)

	want := map[string]string{
		discCallLogs: "Call Log",
		outDMThread:  "DM: Bob Example",
		unknownDM:    "DM: unknown user 0000000a",
		chanGeneral:  "CS Department / General",
		discMeeting:  "Meeting: Standup",
	}
	got := resyncs(t, sink)
	if len(got) != len(want) {
		t.Fatalf("resyncs %v", keys(got))
	}
	rows := threadRows(t, c)
	for threadID, name := range want {
		rs := got[networkid.PortalID(threadID)]
		if rs == nil || strp(rs.ChatInfo.Name) != name || rs.CreatePortal {
			t.Errorf("%s: resync %+v, want name %q", threadID, rs, name)
		}
		if rows[threadID].Name != name {
			t.Errorf("%s: stored %q, want %q", threadID, rows[threadID].Name, name)
		}
	}
	// A channel also gets its team space as parent and its description as topic.
	ch := got[chanGeneral].ChatInfo
	if ch.ParentID == nil || *ch.ParentID != teamPortalID(teamCS) || strp(ch.Topic) != "Course announcements" {
		t.Errorf("channel info %+v", ch)
	}

	// A second pass has nothing to do.
	sink.reset()
	c.applyStructuredRoomNames(ctx, channelMap)
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("second pass: %d events", n)
	}
}
