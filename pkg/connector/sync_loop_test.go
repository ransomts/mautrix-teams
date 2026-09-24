package connector

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// countingAPI counts ListConversations calls.
type countingAPI struct {
	mockTeamsAPI
	lists atomic.Int32
}

func (a *countingAPI) ListConversations(ctx context.Context, token string) ([]model.RemoteConversation, error) {
	a.lists.Add(1)
	return a.mockTeamsAPI.ListConversations(ctx, token)
}

// Graph's answer to GET /teams/{id}/channels?$select=id,displayName,description.
const graphCSChannels = `{
  "@odata.context": "https://graph.microsoft.com/v1.0/$metadata#teams('` + teamCS + `')/channels(id,displayName,description)",
  "@odata.count": 1,
  "value": [
    {"id": "` + chanGeneral + `", "displayName": "General", "description": "Course announcements"}
  ]
}`

func TestSyncOnceRunsEveryPass(t *testing.T) {
	api := &mockTeamsAPI{conversations: []model.RemoteConversation{
		conv(chanGeneral, "TeamsStandardChannel", ""),
		conv(outDMThread, "OneToOneChat", ""),
	}}
	c, sink, _ := newDiscoveryClient(t, api)
	tr := &routeTransport{routes: map[string]http.HandlerFunc{
		"/me/joinedTeams":                jsonRoute(http.StatusOK, `{"value":[{"id":"`+teamCS+`","displayName":"CS Department"}]}`),
		"/teams/" + teamCS + "/channels": jsonRoute(http.StatusOK, graphCSChannels),
	}}
	withGraph(c, tr)
	if err := c.Main.DB.Profile.Upsert(context.Background(), discBob, "Bob Example", time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := c.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}

	// Discovery announced both chats, the team space was synced, and the
	// naming pass renamed both, the channel into its space.
	var names []string
	var spaceSeen bool
	var channelParent networkid.PortalID
	for _, evt := range sink.getEvents() {
		rs, ok := evt.(*simplevent.ChatResync)
		if !ok {
			t.Fatalf("unexpected event %T", evt)
		}
		key, info := rs.PortalKey, rs.ChatInfo
		if key.ID == teamPortalID(teamCS) {
			spaceSeen = true
			continue
		}
		if info.Name != nil {
			names = append(names, string(key.ID)+"="+*info.Name)
		}
		if key.ID == chanGeneral && info.ParentID != nil {
			channelParent = *info.ParentID
		}
	}
	if !spaceSeen {
		t.Error("team space not synced")
	}
	if channelParent != teamPortalID(teamCS) {
		t.Errorf("channel parent %q", channelParent)
	}
	rows := threadRows(t, c)
	if rows[chanGeneral].Name != "CS Department / General" || rows[outDMThread].Name != "DM: Bob Example" {
		t.Errorf("stored names: channel %q, dm %q (announced %v)", rows[chanGeneral].Name, rows[outDMThread].Name, names)
	}
}

func TestSyncOnceStopsWhenDiscoveryFails(t *testing.T) {
	listErr := errors.New("conversations request failed")
	c, sink, _ := newDiscoveryClient(t, &listErrAPI{err: listErr})
	tr := &routeTransport{}
	withGraph(c, tr)
	if err := c.syncOnce(context.Background()); !errors.Is(err, listErr) {
		t.Fatalf("err %v", err)
	}
	// Nothing after discovery ran: no Graph calls, no events.
	if got := tr.seen(); len(got) != 0 {
		t.Errorf("graph requests %v", got)
	}
	if n := len(sink.getEvents()); n != 0 {
		t.Errorf("%d events", n)
	}
}

func TestSyncLoopStartsOnceAndStops(t *testing.T) {
	api := &countingAPI{}
	c, _, _ := newDiscoveryClient(t, api)
	c.startSyncLoop()
	c.startSyncLoop() // already running: no second loop
	deadline := time.Now().Add(5 * time.Second)
	for api.lists.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if api.lists.Load() == 0 {
		t.Fatal("sync loop never ran discovery")
	}
	c.syncMu.Lock()
	done := c.syncDone
	c.syncMu.Unlock()

	c.stopSyncLoop(5 * time.Second)

	select {
	case <-done:
	default:
		t.Fatal("sync goroutines still running after stop")
	}
	// The first run's discovery is the only one: the poll loop's next is
	// threadDiscoveryInterval away.
	if n := api.lists.Load(); n != 1 {
		t.Errorf("discovery ran %d times", n)
	}
}

// ---------------------------------------------------------------------------
// Long-poll
// ---------------------------------------------------------------------------

const longPollRegion = "amer.ng.msg.teams.microsoft.com"

func newLongPollClient(t *testing.T, routes map[string]http.HandlerFunc) (*TeamsClient, *routeTransport) {
	t.Helper()
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	c.Meta.RegionChatServiceURL = "https://" + longPollRegion
	tr := &routeTransport{routes: routes}
	c.consumerHTTP = &http.Client{Transport: tr}
	return c, tr
}

// The enterprise chat service answers endpoint registration with 410 Gone
// (README, "Configuration decisions"); the client then tries the consumer
// endpoint, and the loop gives up, leaving the poll loop alone.
func TestLongPollLoopGivesUpWhenRegistrationFails(t *testing.T) {
	c, tr := newLongPollClient(t, map[string]http.HandlerFunc{
		longPollRegion + "/v1/users/ME/endpoints": jsonRoute(http.StatusGone, `{"errorCode":410,"message":"Gone"}`),
	})
	err := c.longPollLoop(context.Background())
	if err == nil {
		t.Fatal("no error")
	}
	want := []string{
		longPollRegion + "/v1/users/ME/endpoints",
		"teams.live.com/api/chatsvc/consumer/v1/users/ME/endpoints",
	}
	if got := tr.seen(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("requests %v, want %v", got, want)
	}
	if c.longPollUp.Load() {
		t.Error("long-poll marked up")
	}
}

// BUG?: the long-poll event shape is a guess.  PollEvent.Resource is a
// string path here; in Skype-style long-poll responses "resource" is
// usually the message object and the path is in "resourceLink".  The
// loop has never run against the enterprise service (registration fails,
// above), so this pins what the code expects rather than what Teams sends.
const longPollEvents = `{
  "eventMessages": [
    {"id": 1001, "type": "EventMessage", "resourceType": "NewMessage", "time": "2026-09-01T12:00:00Z",
     "resource": "/v1/users/ME/conversations/` + outGroupThread + `/messages/1726000000001"},
    {"id": 1002, "type": "EventMessage", "resourceType": "ConversationUpdate", "time": "2026-09-01T12:00:01Z",
     "resource": "/v1/users/ME/conversations/` + outDMThread + `"},
    {"id": 1003, "type": "EventMessage", "resourceType": "EndpointPresence", "time": "2026-09-01T12:00:02Z",
     "resource": "/v1/users/ME/endpoints/SELF"}
  ]
}`

func TestLongPollLoopWakesNamedThreads(t *testing.T) {
	var polls atomic.Int32
	parked := make(chan struct{})
	var parkOnce sync.Once
	c, _ := newLongPollClient(t, map[string]http.HandlerFunc{
		longPollRegion + "/v1/users/ME/endpoints": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://"+longPollRegion+"/v1/users/ME/endpoints/0000000e-0000-0000-0000-00000000000e")
			w.WriteHeader(http.StatusCreated)
		},
		longPollRegion + "/v1/users/ME/endpoints/0000000e-0000-0000-0000-00000000000e/subscriptions/0/poll": func(w http.ResponseWriter, r *http.Request) {
			if polls.Add(1) == 1 {
				jsonRoute(http.StatusOK, longPollEvents)(w, r)
				return
			}
			// Nothing new: hold the request until the loop is stopped.
			parkOnce.Do(func() { close(parked) })
			<-r.Context().Done()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.longPollLoop(ctx) }()

	select {
	case <-parked:
	case err := <-errc:
		t.Fatalf("loop ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("second long-poll never made")
	}
	if !c.longPollUp.Load() {
		t.Error("long-poll not marked up after a successful poll")
	}
	var wakes []pollWakeup
	for len(c.wakeChan()) > 0 {
		wakes = append(wakes, <-c.wakeChan())
	}
	want := []pollWakeup{
		{threadID: outGroupThread, receipts: false}, // a message: no receipt check
		{threadID: outDMThread, receipts: true},     // anything else may be a read position
	}
	same := len(wakes) == len(want)
	for i := 0; same && i < len(want); i++ {
		// A wakeup also carries when and by what it was noticed.
		same = wakes[i].threadID == want[i].threadID && wakes[i].receipts == want[i].receipts &&
			wakes[i].source == "longpoll" && !wakes[i].at.IsZero()
	}
	if !same {
		t.Errorf("wakeups %+v, want %+v (source longpoll, time set)", wakes, want)
	}

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("loop ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}
