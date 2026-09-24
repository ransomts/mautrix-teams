package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// This file gives tests a real bridgev2.Bridge on a SQLite database, for
// code that reads the bridge's own tables (ghosts, portals, messages).
// The Matrix side is a fake that records the few calls these code paths
// make; any other Matrix call panics on the nil embedded interface, which
// is what a test should do if the code under test starts making it.

// fakeMatrix is the MatrixConnector of the test bridge.
type fakeMatrix struct {
	bridgev2.MatrixConnector

	mu           sync.Mutex
	displayNames map[networkid.UserID]string // ghost ID -> last display name set
	ghostMXIDs   map[networkid.UserID]id.UserID
	deletedRooms []id.RoomID
	bot          *fakeIntent
}

func (m *fakeMatrix) Init(*bridgev2.Bridge) {}

func (m *fakeMatrix) BotIntent() bridgev2.MatrixAPI {
	return m.bot
}

func (m *fakeMatrix) GhostIntent(userID networkid.UserID) bridgev2.MatrixAPI {
	return &fakeIntent{matrix: m, ghostID: userID}
}

// ParseGhostMXID knows the ghosts ghostMXID has named.
func (m *fakeMatrix) ParseGhostMXID(userID id.UserID) (networkid.UserID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ghostID, mxid := range m.ghostMXIDs {
		if mxid == userID {
			return ghostID, true
		}
	}
	return "", false
}

// ghostMXID returns the Matrix ID of the ghost for ghostID.
func (m *fakeMatrix) ghostMXID(ghostID networkid.UserID) id.UserID {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ghostMXIDs == nil {
		m.ghostMXIDs = make(map[networkid.UserID]id.UserID)
	}
	if mxid, ok := m.ghostMXIDs[ghostID]; ok {
		return mxid
	}
	mxid := id.UserID(fmt.Sprintf("@teams_%d:example.org", len(m.ghostMXIDs)+1))
	m.ghostMXIDs[ghostID] = mxid
	return mxid
}

func (m *fakeMatrix) setNames() map[networkid.UserID]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[networkid.UserID]string, len(m.displayNames))
	for k, v := range m.displayNames {
		out[k] = v
	}
	return out
}

func (m *fakeMatrix) deleted() []id.RoomID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]id.RoomID(nil), m.deletedRooms...)
}

// fakeIntent is a ghost's (or the bot's) MatrixAPI.
type fakeIntent struct {
	bridgev2.MatrixAPI
	matrix  *fakeMatrix
	ghostID networkid.UserID
}

func (i *fakeIntent) GetMXID() id.UserID {
	if i.ghostID == "" {
		return "@teamsbot:example.org"
	}
	return i.matrix.ghostMXID(i.ghostID)
}

func (i *fakeIntent) SetDisplayName(_ context.Context, name string) error {
	i.matrix.mu.Lock()
	defer i.matrix.mu.Unlock()
	if i.matrix.displayNames == nil {
		i.matrix.displayNames = make(map[networkid.UserID]string)
	}
	i.matrix.displayNames[i.ghostID] = name
	return nil
}

func (i *fakeIntent) DeleteRoom(_ context.Context, roomID id.RoomID, _ bool) error {
	i.matrix.mu.Lock()
	defer i.matrix.mu.Unlock()
	i.matrix.deletedRooms = append(i.matrix.deletedRooms, roomID)
	return nil
}

// newBridgeTestClient is newTestClient on a real bridge: bridgev2's tables
// and the Teams tables share one SQLite database, as in production.
func newBridgeTestClient(t *testing.T, api TeamsAPI, sink EventSink) (*TeamsClient, *fakeMatrix) {
	t.Helper()
	raw, err := dbutil.NewWithDialect("file:"+filepath.Join(t.TempDir(), "bridge.db")+"?_txlock=immediate", "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	mx := &fakeMatrix{}
	mx.bot = &fakeIntent{matrix: mx}
	connector := &TeamsConnector{}
	br := bridgev2.NewBridge(schedTestBridgeID, raw, zerolog.Nop(), nil, mx, connector,
		func(*bridgev2.Bridge) bridgev2.CommandProcessor { return nil })
	ctx := context.Background()
	if err := br.DB.Upgrade(ctx); err != nil {
		t.Fatalf("bridge db upgrade: %v", err)
	}
	if err := connector.DB.Upgrade(ctx); err != nil {
		t.Fatalf("teams db upgrade: %v", err)
	}

	c := newTestClient(nil, nil)
	c.api = api
	c.events = sink
	c.Main = connector
	return c, mx
}

// insertTestGhost stores a ghost row named name.
func insertTestGhost(t *testing.T, c *TeamsClient, userID, name string) {
	t.Helper()
	g := &database.Ghost{BridgeID: schedTestBridgeID, ID: networkid.UserID(userID), Name: name, NameSet: name != ""}
	if err := c.Main.Bridge.DB.Ghost.Insert(context.Background(), g); err != nil {
		t.Fatalf("insert ghost %s: %v", userID, err)
	}
}

// insertTestPortal stores a portal row.
func insertTestPortal(t *testing.T, c *TeamsClient, p *database.Portal) {
	t.Helper()
	p.BridgeID = schedTestBridgeID
	if err := c.Main.Bridge.DB.Portal.Insert(context.Background(), p); err != nil {
		t.Fatalf("insert portal %s: %v", p.ID, err)
	}
}

// routeTransport answers HTTP requests in-process from routes, keyed by
// host+path or by path alone (the query string dropped, and for Graph the
// /v1.0 prefix too).  Anything else gets a 418, so no test ever reaches
// the network.
type routeTransport struct {
	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []string
}

func (g *routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	if req.URL.Host == "graph.microsoft.com" {
		path = strings.TrimPrefix(path, "/v1.0")
	}
	g.mu.Lock()
	g.requests = append(g.requests, req.URL.Host+path)
	h := g.routes[req.URL.Host+path]
	if h == nil {
		h = g.routes[path]
	}
	g.mu.Unlock()
	rec := httptest.NewRecorder()
	if h == nil {
		rec.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(rec, "no route for "+req.URL.String())
	} else {
		h(rec, req)
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (g *routeTransport) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}

// jsonRoute answers with status and body as application/json.
func jsonRoute(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// withGraph gives c a valid Graph token and sends its HTTP through tr.
func withGraph(c *TeamsClient, tr *routeTransport) {
	c.Meta.GraphAccessToken = "graph-token"
	c.Meta.GraphExpiresAt = time.Now().Add(time.Hour).Unix()
	c.consumerHTTP = &http.Client{Transport: tr}
}
