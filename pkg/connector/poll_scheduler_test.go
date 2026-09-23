package connector

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	// Registers the "sqlite3-fk-wal" driver the bridge uses at runtime.
	_ "go.mau.fi/util/dbutil/litestream"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

const schedTestBridgeID = "teams-test"

// schedAPI is a Teams API fake for driving pollPass: per-conversation
// message pages and errors, a recent-conversations list, and call counts.
type schedAPI struct {
	recentConvsAPI

	mu         sync.Mutex
	pages      map[string][]model.RemoteMessage // conversation -> messages
	errs       map[string]error                 // conversation -> error for every poll
	nextErr    error                            // returned by the next poll only
	polled     []string                         // conversations, in poll order
	discovered int                              // ListConversations calls
}

func (a *schedAPI) ListMessages(_ context.Context, conversationID string, _ string) ([]model.RemoteMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polled = append(a.polled, conversationID)
	if err := a.nextErr; err != nil {
		a.nextErr = nil
		return nil, err
	}
	if err := a.errs[conversationID]; err != nil {
		return nil, err
	}
	return a.pages[conversationID], nil
}

func (a *schedAPI) ListConversations(context.Context, string) ([]model.RemoteConversation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discovered++
	return nil, nil
}

func (a *schedAPI) polls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.polled...)
}

func (a *schedAPI) discoveries() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.discovered
}

// newTestTeamsDB opens a teamsdb on a fresh sqlite file in t.TempDir().
func newTestTeamsDB(t *testing.T) *teamsdb.Database {
	t.Helper()
	raw, err := dbutil.NewWithDialect("file:"+filepath.Join(t.TempDir(), "teams.db")+"?_txlock=immediate", "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := teamsdb.New(schedTestBridgeID, raw, zerolog.Nop())
	if err := db.Upgrade(context.Background()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	return db
}

// newSchedTestClient is newTestClient on a real database holding threads
// (thread ID -> conversation ID).
func newSchedTestClient(t *testing.T, api TeamsAPI, sink EventSink, threads map[string]string) *TeamsClient {
	t.Helper()
	c := newTestClient(nil, nil)
	c.api = api
	c.events = sink
	c.Main.DB = newTestTeamsDB(t)
	for threadID, conv := range threads {
		addTestThread(t, c, &teamsdb.ThreadState{ThreadID: threadID, Conversation: conv})
	}
	return c
}

func addTestThread(t *testing.T, c *TeamsClient, th *teamsdb.ThreadState) {
	t.Helper()
	th.BridgeID = schedTestBridgeID
	th.UserLoginID = c.Login.ID
	if err := c.Main.DB.ThreadState.Upsert(context.Background(), th); err != nil {
		t.Fatalf("add thread: %v", err)
	}
	if th.LastSequenceID != "" {
		if err := c.Main.DB.ThreadState.UpdateCursor(context.Background(), c.Login.ID, th.ThreadID, th.LastSequenceID, th.LastMessageTS); err != nil {
			t.Fatalf("set cursor: %v", err)
		}
	}
}

func savedCursor(t *testing.T, c *TeamsClient, threadID string) string {
	t.Helper()
	th, err := c.Main.DB.ThreadState.Get(context.Background(), c.Login.ID, threadID)
	if err != nil || th == nil {
		t.Fatalf("get thread %s: %v", threadID, err)
	}
	return th.LastSequenceID
}

// newSchedule is the poll loop's starting schedule.
func newSchedule() *pollSchedule {
	return &pollSchedule{lastSeen: make(map[string]string)}
}

func TestPollPassIdleWatchedThreadWaitsForBackstop(t *testing.T) {
	api := &schedAPI{}
	// The recent-conversations list has newest-message IDs: the watch works.
	api.set("conv-a", "m1")
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{"19:a": "conv-a"})
	states := map[string]*pollState{}
	sched := newSchedule()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	next, err := c.pollPass(context.Background(), now, states, sched)
	if err != nil {
		t.Fatal(err)
	}
	if got := api.polls(); len(got) != 1 || got[0] != "conv-a" {
		t.Fatalf("polls = %v, want the due thread once", got)
	}
	ps := states["19:a"]
	if ps == nil || !ps.nextPoll.Equal(now.Add(pollBackstopIdleCap)) {
		t.Fatalf("idle watched thread rescheduled at %+v, want now+%v", ps, pollBackstopIdleCap)
	}
	if next.After(now.Add(pollLoopMaxSleep)) {
		t.Errorf("next wake %v is past the loop's longest sleep", next.Sub(now))
	}

	// Not due again before the backstop.
	if _, err := c.pollPass(context.Background(), now.Add(time.Minute), states, sched); err != nil {
		t.Fatal(err)
	}
	if n := len(api.polls()); n != 1 {
		t.Fatalf("thread polled %d times before its backstop", n)
	}
}

func TestPollPassParksDeletedThread(t *testing.T) {
	api := &schedAPI{errs: map[string]error{
		"conv-gone": consumerclient.MessagesError{Status: http.StatusNotFound, BodySnippet: `{"errorCode":"ThreadNotFound"}`},
	}}
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{"19:gone": "conv-gone"})
	states := map[string]*pollState{}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	if _, err := c.pollPass(context.Background(), now, states, newSchedule()); err != nil {
		t.Fatal(err)
	}
	ps := states["19:gone"]
	if ps == nil || !ps.gone || !ps.nextPoll.Equal(now.Add(goneThreadRecheck)) {
		t.Fatalf("deleted thread state %+v, want parked until now+%v", ps, goneThreadRecheck)
	}
}

func TestPollPassPausesEverythingOn429(t *testing.T) {
	const retryAfter = 45 * time.Second
	api := &schedAPI{nextErr: consumerclient.RetryableError{Status: http.StatusTooManyRequests, RetryAfter: retryAfter}}
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{
		"19:a": "conv-a", "19:b": "conv-b", "19:c": "conv-c",
	})
	states := map[string]*pollState{}
	sched := newSchedule()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	next, err := c.pollPass(context.Background(), now, states, sched)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(api.polls()); n != 1 {
		t.Fatalf("%d polls, want polling to stop at the first 429", n)
	}
	if want := now.Add(retryAfter); !sched.throttledUntil.Equal(want) || !next.Equal(want) {
		t.Fatalf("throttledUntil %v, next %v; want both %v", sched.throttledUntil, next, want)
	}
	for id, ps := range states {
		if ps.nextPoll.After(now) {
			t.Errorf("%s pushed back to %v; the pause alone should hold it", id, ps.nextPoll)
		}
	}

	// Once the pause is over every thread is polled, the throttled one too.
	if _, err := c.pollPass(context.Background(), sched.throttledUntil, states, sched); err != nil {
		t.Fatal(err)
	}
	if n := len(api.polls()); n != 4 {
		t.Fatalf("%d polls after the pause, want all three threads polled", n-1)
	}
}

func TestPollPassUnknownConversationPullsDiscoveryForward(t *testing.T) {
	api := &schedAPI{}
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{"19:a": "conv-a"})
	// The watch is up, so scheduled discovery is discoveryIntervalWatched
	// away and does not interfere.
	c.activityUp.Store(true)
	states := map[string]*pollState{}
	sched := newSchedule()
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	pass := func(at time.Time, newest string) time.Time {
		t.Helper()
		api.set("conv-a", "a1", "19:new", newest)
		next, err := c.pollPass(context.Background(), at, states, sched)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}

	pass(t0, "n1") // discovery runs; the change check only primes
	if n := api.discoveries(); n != 1 {
		t.Fatalf("%d discoveries at start, want 1", n)
	}
	scheduled := sched.nextDiscovery

	// A change in a conversation no thread matches, too soon after the
	// last discovery: left to the schedule.
	pass(t0.Add(activityCheckInterval), "n2")
	if !sched.nextDiscovery.Equal(scheduled) {
		t.Fatalf("discovery pulled to %v within discoveryMinGap", sched.nextDiscovery)
	}

	// Past the gap: discovery is due at once.
	at := t0.Add(discoveryMinGap)
	if next := pass(at, "n3"); !sched.nextDiscovery.Equal(at) || !next.Equal(at) {
		t.Fatalf("nextDiscovery %v, next wake %v; want both %v", sched.nextDiscovery, next, at)
	}
	pass(at, "n3")
	if n := api.discoveries(); n != 2 {
		t.Fatalf("%d discoveries, want the pulled one to run", n)
	}

	// Discovery did not add the conversation; it changes again right away,
	// but discovery is not pulled forward twice within the gap.
	pass(at.Add(activityCheckInterval), "n4")
	if n := api.discoveries(); n != 2 || !sched.nextDiscovery.After(at.Add(discoveryMinGap)) {
		t.Fatalf("%d discoveries, next at %v: pulled forward again within the gap", n, sched.nextDiscovery)
	}
}

func TestPollPassForgetsRemovedThreads(t *testing.T) {
	api := &schedAPI{}
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{"19:a": "conv-a"})
	c.cursors.noteQueued("19:removed", cursorPos{seq: "5"})
	states := map[string]*pollState{"19:removed": {nextPoll: time.Time{}}}

	if _, err := c.pollPass(context.Background(), time.Now().UTC(), states, newSchedule()); err != nil {
		t.Fatal(err)
	}
	if _, ok := states["19:removed"]; ok {
		t.Error("poll state kept for a thread no longer in the database")
	}
	if _, ok := c.cursors.pending("19:removed"); ok {
		t.Error("queued cursor kept for a thread no longer in the database")
	}
	if states["19:a"] == nil {
		t.Error("listed thread has no poll state")
	}
}

func TestPollPassSweepsCaches(t *testing.T) {
	c := newSchedTestClient(t, &schedAPI{}, &capturingEventSink{}, nil)
	sched := newSchedule()
	now := time.Now().UTC()
	c.shouldEmitTyping("19:a", "8:orgid:x")

	if _, err := c.pollPass(context.Background(), now, map[string]*pollState{}, sched); err != nil {
		t.Fatal(err)
	}
	if !sched.nextSweep.Equal(now.Add(cacheSweepInterval)) {
		t.Fatalf("next sweep %v, want now+%v", sched.nextSweep.Sub(now), cacheSweepInterval)
	}
	if len(c.typingSeen) != 1 {
		t.Fatal("fresh typing entry swept")
	}
	if _, err := c.pollPass(context.Background(), sched.nextSweep, map[string]*pollState{}, sched); err != nil {
		t.Fatal(err)
	}
	if len(c.typingSeen) != 0 {
		t.Fatal("expired typing entry not swept")
	}
}

// ---------------------------------------------------------------------------
// Cursor saved only after handling (cursor_commit.go)
// ---------------------------------------------------------------------------

func userMessage(seq string) model.RemoteMessage {
	return model.RemoteMessage{
		MessageID:   "msg-" + seq,
		SequenceID:  seq,
		SenderID:    "8:orgid:other",
		Body:        "hello " + seq,
		MessageType: "RichText/Html",
		Timestamp:   time.Now(),
	}
}

// runPostHandle does what bridgev2's portal loop does once it has handled evt.
func runPostHandle(t *testing.T, evt bridgev2.RemoteEvent) {
	t.Helper()
	ph, ok := evt.(bridgev2.RemotePostHandler)
	if !ok {
		t.Fatalf("%T has no PostHandle", evt)
	}
	ph.PostHandle(context.Background(), nil)
}

func TestCursorSavedOnlyAfterEventsAreHandled(t *testing.T) {
	api := &schedAPI{pages: map[string][]model.RemoteMessage{
		"conv-a": {userMessage("100"), userMessage("101"), userMessage("102")},
	}}
	sink := &capturingEventSink{}
	c := newSchedTestClient(t, api, sink, nil)
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: "19:a", Conversation: "conv-a", LastSequenceID: "100"})
	ctx := context.Background()
	fromDB := func() *teamsdb.ThreadState {
		th, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, "19:a")
		if err != nil {
			t.Fatal(err)
		}
		return th
	}

	if _, err := c.pollThread(ctx, fromDB(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var queued []bridgev2.RemoteEvent
	for _, evt := range sink.getEvents() {
		if evt.GetType() == bridgev2.RemoteEventMessage {
			queued = append(queued, evt)
		}
	}
	if len(queued) != 2 {
		t.Fatalf("%d messages queued, want 101 and 102", len(queued))
	}
	if got := savedCursor(t, c, "19:a"); got != "100" {
		t.Fatalf("cursor saved as %s before bridgev2 handled anything", got)
	}

	// The next pass reads the saved cursor again, but must not queue the
	// page a second time.
	sink.reset()
	if _, err := c.pollThread(ctx, fromDB(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, evt := range sink.getEvents() {
		if evt.GetType() == bridgev2.RemoteEventMessage {
			t.Fatal("queued-but-unsaved page queued again")
		}
	}

	runPostHandle(t, queued[0])
	if got := savedCursor(t, c, "19:a"); got != "100" {
		t.Fatalf("cursor saved as %s with an event still unhandled", got)
	}
	runPostHandle(t, queued[1])
	runPostHandle(t, queued[1]) // a repeated hook counts once
	if got := savedCursor(t, c, "19:a"); got != "102" {
		t.Fatalf("cursor %s after every event was handled, want 102", got)
	}
	if _, ok := c.cursors.pending("19:a"); ok {
		t.Error("queued position kept after the cursor was saved")
	}
}

// handlingSink is an EventSink that handles every event before returning,
// as bridgev2 does with PortalEventBuffer = 0, or refuses it.
type handlingSink struct {
	capturingEventSink
	refuse bool
}

func (s *handlingSink) QueueRemoteEvent(evt bridgev2.RemoteEvent) bridgev2.EventHandlingResult {
	s.capturingEventSink.QueueRemoteEvent(evt)
	if s.refuse {
		return bridgev2.EventHandlingResultIgnored
	}
	if ph, ok := evt.(bridgev2.RemotePostHandler); ok {
		ph.PostHandle(context.Background(), nil)
	}
	return bridgev2.EventHandlingResultSuccess
}

func TestCursorSavedAtOnceWhenNotQueued(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		api := &schedAPI{pages: map[string][]model.RemoteMessage{"conv-a": {userMessage("7"), userMessage("8")}}}
		c := newSchedTestClient(t, api, &handlingSink{refuse: refuse}, map[string]string{"19:a": "conv-a"})
		th, _ := c.Main.DB.ThreadState.Get(context.Background(), c.Login.ID, "19:a")
		if _, err := c.pollThread(context.Background(), th, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := savedCursor(t, c, "19:a"); got != "8" {
			t.Errorf("refuse=%v: cursor %q, want 8", refuse, got)
		}
	}
}

func TestCursorNeverMovesBack(t *testing.T) {
	c := newSchedTestClient(t, &schedAPI{}, &capturingEventSink{}, map[string]string{"19:a": "conv-a"})
	ctx := context.Background()
	c.saveCursor(ctx, "19:a", cursorPos{seq: "20"})
	// A page handled late, e.g. with bridge.async_events.
	c.saveCursor(ctx, "19:a", cursorPos{seq: "10"})
	if got := savedCursor(t, c, "19:a"); got != "20" {
		t.Fatalf("cursor %s, want it to stay at 20", got)
	}
}

// ---------------------------------------------------------------------------
// Cache expiry (cache_sweep.go)
// ---------------------------------------------------------------------------

func TestSweepCachesKeepsLiveEntries(t *testing.T) {
	c := &TeamsClient{}
	now := time.Now()
	c.shouldEmitTyping("19:a", "8:orgid:x")
	c.noteThreadActive("19:a", now)
	c.shouldPollReceipts("19:b", now)
	c.recordNameLookupMiss("8:orgid:gone")
	c.reactionStateChanged("m1", "sig")
	c.markReactionSeen("m1", true)
	c.chatInfoChanged("19:a", "info")

	if n := c.sweepCaches(now); n != 0 {
		t.Fatalf("swept %d fresh entries", n)
	}
	// Past the typing window only typing goes.
	if n := c.sweepCaches(now.Add(typingDedupWindow + time.Second)); n != 1 || len(c.typingSeen) != 0 {
		t.Fatalf("swept %d at the typing window, typing left %d", n, len(c.typingSeen))
	}
	// Past every TTL everything goes.
	c.sweepCaches(now.Add(max(reactionStateTTL, chatInfoTTL, nameLookupMissTTL) + time.Second))
	for name, n := range map[string]int{
		"threadActive": len(c.threadActive), "receiptPoll": len(c.receiptPoll),
		"nameLookupMiss": len(c.nameLookupMiss), "reactionSigs": len(c.reactionSigs),
		"reactionSeen": len(c.reactionSeen), "chatInfoSigs": len(c.chatInfoSigs),
	} {
		if n != 0 {
			t.Errorf("%s kept %d expired entries", name, n)
		}
	}
}

// Every TTL is at least the window in which its entry still matters, so an
// entry swept at its TTL behaves as it would have unswept.
func TestSweptEntriesBehaveAsExpired(t *testing.T) {
	c := &TeamsClient{}
	now := time.Now()
	c.shouldPollReceipts("19:a", now)
	c.noteThreadActive("19:b", now)
	later := now.Add(receiptIdlePollInterval + time.Second)
	c.sweepCaches(later)
	if !c.shouldPollReceipts("19:a", later) {
		t.Error("a swept receipt check is not due")
	}
	later = now.Add(threadActiveWindow + time.Second)
	c.sweepCaches(later)
	if c.threadIsActive("19:b", later) {
		t.Error("a swept thread still active")
	}
}

func TestUnreadEntryGoesOnceReceiptSent(t *testing.T) {
	c := &TeamsClient{}
	if c.shouldSendReceipt("19:a") {
		t.Fatal("receipt for a thread with nothing unread")
	}
	c.markUnread("19:a")
	if !c.shouldSendReceipt("19:a") || c.shouldSendReceipt("19:a") {
		t.Fatal("want exactly one receipt per run of unread messages")
	}
	if len(c.unreadSeen) != 0 {
		t.Fatal("entry kept after its receipt")
	}
	c.markUnread("19:a")
	if !c.shouldSendReceipt("19:a") {
		t.Fatal("new unread messages get no receipt")
	}
}

// ---------------------------------------------------------------------------
// Sync goroutine lifecycle (sync_loop.go)
// ---------------------------------------------------------------------------

func newLifecycleClient() *TeamsClient {
	return &TeamsClient{Login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{}, Log: zerolog.Nop()}}
}

func TestStopSyncLoopWaitsForEveryGoroutine(t *testing.T) {
	c := newLifecycleClient()
	var childDone atomic.Bool
	c.startSyncTasks(func(ctx context.Context, tasks *syncTasks) {
		// A child like the long-poll loop: outlives the root, and takes a
		// moment to notice the cancellation.
		tasks.Go(func() {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			childDone.Store(true)
		})
	})
	c.stopSyncLoop(5 * time.Second)
	if !childDone.Load() {
		t.Fatal("stopSyncLoop returned before a child goroutine ended")
	}
}

func TestStopSyncLoopGivesUpOnStuckGoroutine(t *testing.T) {
	c := newLifecycleClient()
	release := make(chan struct{})
	defer close(release)
	c.startSyncTasks(func(_ context.Context, tasks *syncTasks) {
		tasks.Go(func() { <-release }) // ignores cancellation
	})
	const timeout = 100 * time.Millisecond
	start := time.Now()
	c.stopSyncLoop(timeout)
	if elapsed := time.Since(start); elapsed < timeout || elapsed > 5*time.Second {
		t.Fatalf("stopSyncLoop took %v with a stuck goroutine, want about %v", elapsed, timeout)
	}
	// Stopped means stopped: a new loop can start.
	started := make(chan struct{})
	c.startSyncTasks(func(context.Context, *syncTasks) { close(started) })
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("sync loop did not restart after a timed-out stop")
	}
	c.stopSyncLoop(time.Second)
}

// ---------------------------------------------------------------------------
// Reaction sync order (reaction_sync.go)
// ---------------------------------------------------------------------------

func TestReactionSyncChecksSignatureBeforeLookup(t *testing.T) {
	var lookups int
	orig := resolveReactionTarget
	resolveReactionTarget = func(c *TeamsClient, ctx context.Context, threadID, messageID string, msg model.RemoteMessage) string {
		lookups++
		return orig(c, ctx, threadID, messageID, msg)
	}
	defer func() { resolveReactionTarget = orig }()

	sink := &capturingEventSink{}
	c := newTestClient(&mockTeamsAPI{}, sink)
	th := newTestThreadState()
	msg := userMessage("5")
	msg.Reactions = []model.MessageReaction{{EmotionKey: "like", Users: []model.MessageReactionUser{{MRI: "8:orgid:x", TimeMS: 1}}}}

	for i := 0; i < 3; i++ {
		c.queueReactionSyncForMessage(context.Background(), th, msg, "5")
	}
	if lookups != 1 || len(sink.getEvents()) != 1 {
		t.Fatalf("%d lookups, %d syncs for one unchanged reaction set; want 1 and 1", lookups, len(sink.getEvents()))
	}
	msg.Reactions[0].Users = append(msg.Reactions[0].Users, model.MessageReactionUser{MRI: "8:orgid:y", TimeMS: 2})
	c.queueReactionSyncForMessage(context.Background(), th, msg, "5")
	if lookups != 2 || len(sink.getEvents()) != 2 {
		t.Fatalf("%d lookups, %d syncs after a change; want 2 and 2", lookups, len(sink.getEvents()))
	}
}
