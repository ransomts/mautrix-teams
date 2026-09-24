package connector

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	ghostAnn = "8:orgid:00000001-0000-0000-0000-000000000001"
	ghostBob = "8:orgid:00000002-0000-0000-0000-000000000002"
)

// Graph's answer to GET /users/{id}?$select=id,displayName,mail,userPrincipalName.
const graphUserBob = `{
  "@odata.context": "https://graph.microsoft.com/v1.0/$metadata#users(id,displayName,mail,userPrincipalName)/$entity",
  "id": "00000002-0000-0000-0000-000000000002",
  "displayName": "Bob Example",
  "mail": "bob@example.edu",
  "userPrincipalName": "bob@example.edu"
}`

// Graph's answer for an object ID it does not know (a deleted account).
const graphUserNotFound = `{
  "error": {
    "code": "Request_ResourceNotFound",
    "message": "Resource '00000002-0000-0000-0000-000000000002' does not exist or one of its queried reference-property objects are not present."
  }
}`

func TestSyncGhostNameRenamesExistingGhost(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	ctx := context.Background()
	insertTestGhost(t, c, ghostAnn, ghostAnn) // named after its ID

	c.syncGhostName(ctx, ghostAnn, "  Ann Example ")

	if got := mx.setNames()[ghostAnn]; got != "Ann Example" {
		t.Fatalf("display name set to %q, want %q", got, "Ann Example")
	}
	g, err := c.Main.Bridge.DB.Ghost.GetByID(ctx, ghostAnn)
	if err != nil || g == nil || g.Name != "Ann Example" || !g.NameSet {
		t.Fatalf("ghost row not updated: %+v, %v", g, err)
	}
}

func TestSyncGhostNameNoOps(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	ctx := context.Background()
	insertTestGhost(t, c, ghostAnn, "Ann Example")

	c.syncGhostName(ctx, ghostAnn, "Ann Example") // unchanged
	c.syncGhostName(ctx, ghostAnn, "")            // unknown
	c.syncGhostName(ctx, ghostAnn, ghostAnn)      // the ID is not a name
	c.syncGhostName(ctx, "", "Nobody")
	c.syncGhostName(ctx, ghostBob, "Bob Example") // no ghost yet

	if names := mx.setNames(); len(names) != 0 {
		t.Fatalf("unexpected renames: %v", names)
	}
	// A missing ghost is left for GetUserInfo to create and name.
	if g, _ := c.Main.Bridge.DB.Ghost.GetByID(ctx, ghostBob); g != nil {
		t.Fatalf("syncGhostName created a ghost: %+v", g)
	}
}

func TestSyncGhostNameNilBridge(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	c.syncGhostName(context.Background(), ghostAnn, "Ann Example") // must not panic
}

func TestSyncGhostNamesFromProfilesUsesProfilesThenGraph(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	ctx := context.Background()
	tr := &routeTransport{routes: map[string]http.HandlerFunc{
		"/users/00000002-0000-0000-0000-000000000002": jsonRoute(http.StatusOK, graphUserBob),
	}}
	withGraph(c, tr)

	// Ann's profile is known; Bob was only ever seen through a reaction,
	// so his ghost is named after his ID and no profile row exists.  A bot
	// ghost named after its ID is not a directory user and is not looked up.
	insertTestGhost(t, c, ghostAnn, ghostAnn)
	insertTestGhost(t, c, ghostBob, ghostBob)
	insertTestGhost(t, c, "28:bot-00000003", "28:bot-00000003")
	if err := c.Main.DB.Profile.Upsert(ctx, ghostAnn, "Ann Example", time.Now()); err != nil {
		t.Fatal(err)
	}

	c.syncGhostNamesFromProfiles(ctx)

	names := mx.setNames()
	if names[ghostAnn] != "Ann Example" || names[ghostBob] != "Bob Example" {
		t.Fatalf("ghost names: %v", names)
	}
	if _, ok := names["28:bot-00000003"]; ok {
		t.Fatal("bot ghost renamed")
	}
	// Ann came from the profile table, so Graph was asked only about Bob.
	if got := tr.seen(); len(got) != 1 {
		t.Fatalf("graph requests: %v", got)
	}
	// Graph's answer is stored for next time.
	if p, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, ghostBob); err != nil || p == nil || p.DisplayName != "Bob Example" {
		t.Fatalf("Bob's profile not stored: %+v, %v", p, err)
	}
}

func TestIDNamedGhostsListsDirectoryUsersOnly(t *testing.T) {
	c, _ := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	insertTestGhost(t, c, ghostAnn, ghostAnn)
	insertTestGhost(t, c, ghostBob, "Bob Example")
	insertTestGhost(t, c, "8:live:someone", "8:live:someone")

	got := c.idNamedGhosts(context.Background())
	if !slices.Equal(got, []string{ghostAnn}) {
		t.Fatalf("idNamedGhosts = %v, want [%s]", got, ghostAnn)
	}
}

func TestSyncGhostNamesFromProfilesRemembersUnknownUsers(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	ctx := context.Background()
	tr := &routeTransport{routes: map[string]http.HandlerFunc{
		"/users/00000002-0000-0000-0000-000000000002": jsonRoute(http.StatusNotFound, graphUserNotFound),
	}}
	withGraph(c, tr)
	insertTestGhost(t, c, ghostBob, ghostBob)

	c.syncGhostNamesFromProfiles(ctx)
	c.syncGhostNamesFromProfiles(ctx)

	if names := mx.setNames(); len(names) != 0 {
		t.Fatalf("deleted user renamed: %v", names)
	}
	// The miss is remembered: the second pass does not ask again.
	if got := tr.seen(); len(got) != 1 {
		t.Fatalf("graph asked %d times about a deleted user: %v", len(got), got)
	}
	g, _ := c.Main.Bridge.DB.Ghost.GetByID(ctx, networkid.UserID(ghostBob))
	if g == nil || g.Name != ghostBob {
		t.Fatalf("ghost changed: %+v", g)
	}
}

func TestSyncGhostNamesFromProfilesStopsOnCancel(t *testing.T) {
	c, mx := newBridgeTestClient(t, &mockTeamsAPI{}, &capturingEventSink{})
	ctx, cancel := context.WithCancel(context.Background())
	insertTestGhost(t, c, ghostAnn, ghostAnn)
	if err := c.Main.DB.Profile.Upsert(context.Background(), ghostAnn, "Ann Example", time.Now()); err != nil {
		t.Fatal(err)
	}
	cancel()
	c.syncGhostNamesFromProfiles(ctx)
	if names := mx.setNames(); len(names) != 0 {
		t.Fatalf("renamed after cancel: %v", names)
	}
}
