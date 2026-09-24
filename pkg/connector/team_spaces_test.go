package connector

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

// Team (group) IDs are Azure AD object IDs.
const (
	teamCS   = "00000000-0000-0000-0000-00000000c500"
	teamMath = "00000000-0000-0000-0000-00000000a700"
	teamLab  = "00000000-0000-0000-0000-00000000fab0"

	chanGeneral = "19:0123456789abcdef0123456789abcdef@thread.tacv2"
	chanRandom  = "19:1123456789abcdef0123456789abcdef@thread.tacv2"
	chanMath    = "19:2123456789abcdef0123456789abcdef@thread.tacv2"
	chanLab     = "19:3123456789abcdef0123456789abcdef@thread.tacv2"
	chanNoTeam  = "19:4123456789abcdef0123456789abcdef@thread.tacv2"
)

// Graph's answer to GET /me/joinedTeams?$select=id,displayName.  The lab
// team comes back without a name; the math team is missing altogether.
const graphJoinedTeams = `{
  "@odata.context": "https://graph.microsoft.com/v1.0/$metadata#teams(id,displayName)",
  "@odata.count": 2,
  "value": [
    {"id": "` + teamCS + `", "displayName": "CS Department"},
    {"id": "` + teamLab + `", "displayName": ""}
  ]
}`

// resyncs returns the ChatResync events queued on sink, by portal ID.
func resyncs(t *testing.T, sink *capturingEventSink) map[networkid.PortalID]*simplevent.ChatResync {
	t.Helper()
	out := make(map[networkid.PortalID]*simplevent.ChatResync)
	for _, evt := range sink.getEvents() {
		rs, ok := evt.(*simplevent.ChatResync)
		if !ok {
			t.Fatalf("unexpected event %T", evt)
		}
		if _, dup := out[rs.PortalKey.ID]; dup {
			t.Fatalf("two resyncs for %s", rs.PortalKey.ID)
		}
		out[rs.PortalKey.ID] = rs
	}
	return out
}

func TestSyncTeamSpacesNamesFromGraphThenChannels(t *testing.T) {
	sink := &capturingEventSink{}
	c := newTestClient(&mockTeamsAPI{}, sink)
	withGraph(c, &routeTransport{routes: map[string]http.HandlerFunc{
		"/me/joinedTeams": jsonRoute(http.StatusOK, graphJoinedTeams),
	}})
	channelMap := map[string]graph.ChannelInfo{
		// A channel of a team Graph named: the Graph name wins.
		chanGeneral: {TeamID: teamCS, TeamName: "Old CS name", ChannelName: "General"},
		// A team me/joinedTeams omitted: named from the channel map.
		chanMath:   {TeamID: teamMath, TeamName: "Math", ChannelName: "General"},
		chanNoTeam: {ChannelName: "Orphan"},
	}

	c.syncTeamSpaces(context.Background(), channelMap)

	got := resyncs(t, sink)
	want := map[string]string{teamCS: "CS Department", teamMath: "Math", teamLab: "Team"}
	if len(got) != len(want) {
		t.Fatalf("resyncs for %v, want teams %v", keys(got), want)
	}
	for teamID, name := range want {
		rs := got[teamPortalID(teamID)]
		if rs == nil {
			t.Fatalf("no resync for team %s", teamID)
		}
		// Unscoped: channels reference their space with an empty receiver.
		if rs.PortalKey.Receiver != "" || !rs.CreatePortal || rs.Type != bridgev2.RemoteEventChatResync {
			t.Errorf("%s: event meta %+v", teamID, rs.EventMeta)
		}
		if rs.ChatInfo == nil || rs.ChatInfo.Name == nil || *rs.ChatInfo.Name != name ||
			rs.ChatInfo.Type == nil || *rs.ChatInfo.Type != database.RoomTypeSpace {
			t.Errorf("%s: chat info %+v", teamID, rs.ChatInfo)
		}
	}
	// Real names are cached for teamSpaceChatInfo; the "Team" fallback is not.
	if c.cachedTeamName(teamCS) != "CS Department" || c.cachedTeamName(teamMath) != "Math" || c.cachedTeamName(teamLab) != "" {
		t.Errorf("cache: %v", c.teamNames)
	}
}

func TestSyncTeamSpacesWithoutGraphUsesChannelMap(t *testing.T) {
	sink := &capturingEventSink{}
	c := newTestClient(&mockTeamsAPI{}, sink) // no Graph token
	c.syncTeamSpaces(context.Background(), map[string]graph.ChannelInfo{
		chanMath: {TeamID: teamMath, TeamName: "Math"},
	})
	got := resyncs(t, sink)
	if rs := got[teamPortalID(teamMath)]; len(got) != 1 || rs == nil || *rs.ChatInfo.Name != "Math" {
		t.Fatalf("resyncs %v", keys(got))
	}

	// Graph failing is the same as no Graph.
	sink.reset()
	withGraph(c, &routeTransport{routes: map[string]http.HandlerFunc{
		"/me/joinedTeams": jsonRoute(http.StatusForbidden, `{"error":{"code":"Forbidden","message":"Missing scope permissions on the request."}}`),
	}})
	c.syncTeamSpaces(context.Background(), map[string]graph.ChannelInfo{chanMath: {TeamID: teamMath, TeamName: "Math"}})
	if got := resyncs(t, sink); len(got) != 1 {
		t.Fatalf("resyncs %v", keys(got))
	}

	// Nothing known: nothing announced.
	sink.reset()
	c.syncTeamSpaces(context.Background(), nil)
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("%d events with no teams", n)
	}
}

func keys[V any](m map[networkid.PortalID]V) []networkid.PortalID {
	var out []networkid.PortalID
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestIsTeamPortalID(t *testing.T) {
	if !isTeamPortalID(teamPortalID(" " + teamCS + " ")) {
		t.Error("team portal ID not recognised")
	}
	if teamPortalID(" "+teamCS+" ") != networkid.PortalID("team:"+teamCS) {
		t.Error("team portal ID not trimmed")
	}
	if isTeamPortalID(chanGeneral) {
		t.Error("channel taken for a team")
	}
}

// ---------------------------------------------------------------------------
// repairTeamSpaces
// ---------------------------------------------------------------------------

func getPortalRow(t *testing.T, c *TeamsClient, key networkid.PortalKey) *database.Portal {
	t.Helper()
	p, err := c.Main.Bridge.DB.Portal.GetByKey(context.Background(), key)
	if err != nil {
		t.Fatalf("get portal %v: %v", key, err)
	}
	return p
}

func TestRepairTeamSpaces(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	login := testLoginID
	space := database.RoomTypeSpace

	// An orphan login-scoped twin with a room: deleted with its room.
	orphan := networkid.PortalKey{ID: teamPortalID(teamCS), Receiver: login}
	insertTestPortal(t, c, &database.Portal{PortalKey: orphan, MXID: "!orphan:example.org", RoomType: space})
	// A login-scoped team portal with a child: left alone.
	scopedParent := networkid.PortalKey{ID: teamPortalID(teamLab), Receiver: login}
	insertTestPortal(t, c, &database.Portal{PortalKey: scopedParent, MXID: "!scoped:example.org", RoomType: space})
	insertTestPortal(t, c, &database.Portal{PortalKey: networkid.PortalKey{ID: chanLab, Receiver: login}, ParentKey: scopedParent, InSpace: true})
	// An unscoped team portal whose room is a plain room, with two
	// channels: the room goes, the portal becomes a roomless space, and the
	// channel that was in the old room must join the new one.
	plain := networkid.PortalKey{ID: teamPortalID(teamMath)}
	insertTestPortal(t, c, &database.Portal{PortalKey: plain, MXID: "!plain:example.org"})
	mathKey := networkid.PortalKey{ID: chanMath, Receiver: login}
	insertTestPortal(t, c, &database.Portal{PortalKey: mathKey, MXID: "!math:example.org", ParentKey: plain, InSpace: true})
	randomKey := networkid.PortalKey{ID: chanRandom, Receiver: login}
	insertTestPortal(t, c, &database.Portal{PortalKey: randomKey, ParentKey: plain})
	// A healthy space and a channel: untouched.
	healthy := networkid.PortalKey{ID: teamPortalID("00000000-0000-0000-0000-0000000000ok")}
	insertTestPortal(t, c, &database.Portal{PortalKey: healthy, MXID: "!space:example.org", RoomType: space})
	general := networkid.PortalKey{ID: chanGeneral, Receiver: login}
	insertTestPortal(t, c, &database.Portal{PortalKey: general, MXID: "!general:example.org", ParentKey: healthy, InSpace: true})

	c.repairTeamSpaces(context.Background())

	deleted := mx.deleted()
	slices.Sort(deleted)
	if want := []id.RoomID{"!orphan:example.org", "!plain:example.org"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted rooms %v, want %v", deleted, want)
	}
	if p := getPortalRow(t, c, orphan); p != nil {
		t.Errorf("orphan portal kept: %+v", p)
	}
	if p := getPortalRow(t, c, scopedParent); p == nil || p.MXID != "!scoped:example.org" {
		t.Errorf("scoped team portal with a child changed: %+v", p)
	}
	if p := getPortalRow(t, c, plain); p == nil || p.MXID != "" || p.RoomType != space {
		t.Errorf("plain team portal not detached: %+v", p)
	}
	if p := getPortalRow(t, c, mathKey); p == nil || p.InSpace || p.ParentKey != plain {
		t.Errorf("channel of detached team: %+v", p)
	}
	if p := getPortalRow(t, c, healthy); p == nil || p.MXID != "!space:example.org" {
		t.Errorf("healthy space changed: %+v", p)
	}
	if p := getPortalRow(t, c, general); p == nil || !p.InSpace {
		t.Errorf("healthy channel changed: %+v", p)
	}
}

// ---------------------------------------------------------------------------
// syncChannelParents
// ---------------------------------------------------------------------------

func TestSyncChannelParentsReparentsWrongParent(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, &mockTeamsAPI{}, sink)
	login := testLoginID
	space := database.RoomTypeSpace
	csSpace := networkid.PortalKey{ID: teamPortalID(teamCS)}
	insertTestPortal(t, c, &database.Portal{PortalKey: csSpace, MXID: "!cs:example.org", RoomType: space})
	oldSpace := networkid.PortalKey{ID: teamPortalID(teamLab)}
	insertTestPortal(t, c, &database.Portal{PortalKey: oldSpace, MXID: "!lab:example.org", RoomType: space})

	portals := []*database.Portal{
		// No parent yet: reparented.
		{PortalKey: networkid.PortalKey{ID: chanGeneral, Receiver: login}, MXID: "!general:example.org"},
		// Under another team's space: reparented.
		{PortalKey: networkid.PortalKey{ID: chanRandom, Receiver: login}, MXID: "!random:example.org", ParentKey: oldSpace, InSpace: true},
		// Already right and in its space: left alone.
		{PortalKey: networkid.PortalKey{ID: chanMath, Receiver: login}, MXID: "!math:example.org", ParentKey: csSpace, InSpace: true},
		// No room yet: bridgev2 parents it when it creates the room.
		{PortalKey: networkid.PortalKey{ID: chanLab, Receiver: login}},
		// Graph has it without a team: nothing to parent it to.
		{PortalKey: networkid.PortalKey{ID: chanNoTeam, Receiver: login}, MXID: "!noteam:example.org"},
	}
	for _, p := range portals {
		insertTestPortal(t, c, p)
		addTestThread(t, c, &teamsdb.ThreadState{ThreadID: string(p.ID), Conversation: string(p.ID)})
	}
	// A chat that is no channel at all.
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: outGroupThread, Conversation: outGroupThread})

	channelMap := map[string]graph.ChannelInfo{
		chanGeneral: {TeamID: teamCS, TeamName: "CS Department", ChannelName: "General"},
		chanRandom:  {TeamID: teamCS, TeamName: "CS Department", ChannelName: "Random"},
		chanMath:    {TeamID: teamCS, TeamName: "CS Department", ChannelName: "Math"},
		chanLab:     {TeamID: teamCS, TeamName: "CS Department", ChannelName: "Lab"},
		chanNoTeam:  {ChannelName: "Orphan"},
	}
	c.syncChannelParents(context.Background(), channelMap)

	got := resyncs(t, sink)
	if want := []networkid.PortalID{chanGeneral, chanRandom}; !slices.Equal(keys(got), want) {
		t.Fatalf("reparented %v, want %v", keys(got), want)
	}
	for _, rs := range got {
		if rs.PortalKey.Receiver != login || rs.CreatePortal {
			t.Errorf("event meta %+v", rs.EventMeta)
		}
		if rs.ChatInfo.ParentID == nil || *rs.ChatInfo.ParentID != csSpace.ID || rs.ChatInfo.Name != nil {
			t.Errorf("chat info %+v", rs.ChatInfo)
		}
	}
}

func TestSyncChannelParentsNeedsChannelMap(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, &mockTeamsAPI{}, sink)
	insertTestPortal(t, c, &database.Portal{PortalKey: networkid.PortalKey{ID: chanGeneral, Receiver: testLoginID}, MXID: "!general:example.org"})
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: chanGeneral, Conversation: chanGeneral})
	// Graph unavailable (nil map): no channel is touched.
	c.syncChannelParents(context.Background(), nil)
	if n := len(sink.getEvents()); n != 0 {
		t.Fatalf("%d events without a channel map", n)
	}
}
