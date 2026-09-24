package connector

// Saving a thread's poll cursor only after bridgev2 has handled what the
// poll queued.
//
// bridgev2 does not handle a remote event when it is queued: by default
// (bridgev2.PortalEventBuffer = 64) Portal.queueEvent puts it on the portal's
// channel and returns EventHandlingResultQueued, and the portal's own event
// loop handles it later. Saving the cursor as soon as a page was queued lost
// every message still in a portal queue when the bridge crashed or was
// restarted in between. The cursor is now saved from the events' PostHandle
// hook (simplevent.EventMeta.PostHandleFunc), which Portal.handleRemoteEvent
// calls once the handler has run, whether it succeeded or not, and only when
// every event queued from the page has been through it.
//
// Handling again after a crash is safe: bridgev2 drops a message whose ID it
// already has (Portal.handleRemoteMessage), and an edit bridged twice only
// repeats an identical replacement. An event bridgev2 drops without handling
// (an edit or delete for a chat that has no room) never calls the hook; its
// page's cursor is then not saved until a later page's is, which at worst
// polls that page once more after a restart.
//
// Until a page's cursor is saved the poller keeps the queued position in
// memory, so its next pass does not queue the same page again.

import (
	"context"
	"sync"
	"sync/atomic"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// cursorPos is a position in a thread: the newest sequence ID and its
// message's timestamp.
type cursorPos struct {
	seq string
	ts  int64
}

// newerThan reports whether p is past other.
func (p cursorPos) newerThan(other string) bool {
	return p.seq != "" && (other == "" || model.CompareSequenceID(p.seq, other) > 0)
}

// threadCursors holds each thread's queued-but-unsaved position and the
// newest position this client saved.
type threadCursors struct {
	mu     sync.Mutex
	queued map[string]cursorPos // threadID -> newest position queued, not yet saved
	saved  map[string]string    // threadID -> newest sequence ID saved
	// saveMu keeps each check-and-write whole, so a page whose handling
	// finishes late (bridge.async_events) cannot move the cursor back.
	saveMu sync.Mutex
}

// pending returns the position queued for threadID that is not saved yet.
func (tc *threadCursors) pending(threadID string) (cursorPos, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	pos, ok := tc.queued[threadID]
	return pos, ok
}

// noteQueued records that everything up to pos has been queued.
func (tc *threadCursors) noteQueued(threadID string, pos cursorPos) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if prev, ok := tc.queued[threadID]; ok && !pos.newerThan(prev.seq) {
		return
	}
	if tc.queued == nil {
		tc.queued = make(map[string]cursorPos)
	}
	tc.queued[threadID] = pos
}

// needsSave reports whether pos is newer than what was saved for threadID.
func (tc *threadCursors) needsSave(threadID string, pos cursorPos) bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return pos.newerThan(tc.saved[threadID])
}

// noteSaved records pos as threadID's saved cursor; the queued entry goes
// once the saved cursor has caught up with it.
func (tc *threadCursors) noteSaved(threadID string, pos cursorPos) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.saved == nil {
		tc.saved = make(map[string]string)
	}
	tc.saved[threadID] = pos.seq
	if q, ok := tc.queued[threadID]; ok && !q.newerThan(pos.seq) {
		delete(tc.queued, threadID)
	}
}

// forget drops threadID's entries, for a thread no longer polled.
func (tc *threadCursors) forget(threadID string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	delete(tc.queued, threadID)
	delete(tc.saved, threadID)
}

// cursorCommit saves one poll page's cursor once every event queued from
// the page has been handled. The page itself holds one count until it has
// queued everything, so a fast handler cannot save it early.
type cursorCommit struct {
	pending atomic.Int32
	// save is set by release, before the page's own count is given up, so
	// the hook that sees the count reach zero always finds it.
	save func(ctx context.Context)
}

func newCursorCommit() *cursorCommit {
	cc := &cursorCommit{}
	cc.pending.Store(1)
	return cc
}

// track counts one more event and returns the hook that marks it handled.
// The hook acts once, however often it is called.
func (cc *cursorCommit) track() func(context.Context, *bridgev2.Portal) {
	cc.pending.Add(1)
	var once sync.Once
	return func(ctx context.Context, _ *bridgev2.Portal) {
		once.Do(func() { cc.done(ctx) })
	}
}

// release gives up the page's own count once the page is queued; save runs
// when the last of its events has been handled, which may be at once.
func (cc *cursorCommit) release(ctx context.Context, save func(ctx context.Context)) {
	cc.save = save
	cc.done(ctx)
}

func (cc *cursorCommit) done(ctx context.Context) {
	if cc.pending.Add(-1) == 0 {
		cc.save(ctx)
	}
}

// queueForCursor queues evt, whose metadata is meta, so that its handling
// counts towards commit.
func (c *TeamsClient) queueForCursor(ctx context.Context, evt bridgev2.RemoteEvent, meta *simplevent.EventMeta, commit *cursorCommit) {
	handled := commit.track()
	if also := meta.PostHandleFunc; also != nil {
		// The caller's own hook (the latency log) runs after the cursor's.
		meta.PostHandleFunc = func(ctx context.Context, portal *bridgev2.Portal) {
			handled(ctx, portal)
			also(ctx, portal)
		}
	} else {
		meta.PostHandleFunc = handled
	}
	// Not queued: bridgev2 either handled it already (PortalEventBuffer 0,
	// where PostHandle has run) or refused it (no portal), where PostHandle
	// never will. Either way it is finished with.
	if res := c.queueRemoteEvent(evt); !res.Queued {
		handled(ctx, nil)
	}
}

// saveCursor persists pos as threadID's cursor unless a newer one is saved.
func (c *TeamsClient) saveCursor(ctx context.Context, threadID string, pos cursorPos) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return
	}
	c.cursors.saveMu.Lock()
	defer c.cursors.saveMu.Unlock()
	if !c.cursors.needsSave(threadID, pos) {
		return
	}
	// The poll may have been cancelled (Disconnect) since it queued the
	// page; the events are bridged, so record that regardless.
	if err := c.Main.DB.ThreadState.UpdateCursor(context.WithoutCancel(ctx), c.Login.ID, threadID, pos.seq, pos.ts); err != nil {
		// The queued position stays in memory, so polling carries on from
		// it; the next page's save covers this one.
		log := c.log()
		log.Warn().Err(err).Str("thread_id", threadID).Str("seq", pos.seq).Msg("Failed to save thread cursor")
		return
	}
	c.cursors.noteSaved(threadID, pos)
}
