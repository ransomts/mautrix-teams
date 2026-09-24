package connector

// Waking the poll loop early, and how often threads and read positions are
// polled.
//
// bridgev2 sends the delivery receipt for an outgoing message only once the
// message comes back from Teams (the remote echo), so the thread is polled
// right after every send instead of whenever its backoff comes round.
// Incoming messages are noticed by listing the most recently active
// conversations every few seconds (one request for all of them) and waking
// each thread whose newest message changed; long-poll notifications, where
// the chat service still offers them (consumer accounts), wake threads the
// same way. Per-thread polling then only backs these up. The poll loop stays
// the only caller of pollThread, so a thread is never polled twice at once.

import (
	"context"
	"time"
)

const (
	// pollBackstopIdleCap is the idle poll cap while changes are being
	// watched: polling then only catches what the watch missed, such as a
	// reaction or edit on an older message in a quiet chat. At a few hundred
	// threads it is most of the bridge's request load.
	pollBackstopIdleCap = 15 * time.Minute
	// activeThreadIdleCap is the idle poll cap of a thread with a recent own
	// send, whose read positions are checked on every poll.
	activeThreadIdleCap = 5 * time.Second
	// activityCheckInterval spaces the recent-conversations checks.
	activityCheckInterval = 3 * time.Second
	// activityPageSize is how many recent conversations each check lists.
	activityPageSize = 30
	// threadActiveWindow is how long after an own send a thread counts as
	// active, i.e. someone is likely to read the message soon.
	threadActiveWindow = 30 * time.Minute
	// receiptIdlePollInterval spaces read-position checks for other threads.
	receiptIdlePollInterval = 2 * time.Minute
	// wakeQueueSize bounds queued wakeups; a dropped one is covered by the
	// regular cadence.
	wakeQueueSize = 256
)

type pollWakeup struct {
	threadID string
	receipts bool      // also check read positions now
	at       time.Time // when the change was noticed, for the latency log
	source   string    // what noticed it: watch, send or longpoll
}

func (c *TeamsClient) wakeChan() chan pollWakeup {
	c.pollWakeOnce.Do(func() { c.pollWake = make(chan pollWakeup, wakeQueueSize) })
	return c.pollWake
}

// requestPoll asks the poll loop to poll threadID (a thread or conversation
// ID) now. It never blocks.
func (c *TeamsClient) requestPoll(threadID string, receipts bool, source string) {
	if c == nil || threadID == "" {
		return
	}
	select {
	case c.wakeChan() <- pollWakeup{threadID: threadID, receipts: receipts, at: time.Now(), source: source}:
	default:
	}
}

// noteThreadActive records an own send to threadID: its read positions are
// then checked on every poll for a while, so "seen" follows quickly.
func (c *TeamsClient) noteThreadActive(threadID string, now time.Time) {
	c.receiptPollMu.Lock()
	defer c.receiptPollMu.Unlock()
	if c.threadActive == nil {
		c.threadActive = make(map[string]time.Time)
	}
	c.threadActive[threadID] = now
	delete(c.receiptPoll, threadID)
}

// forgetReceiptPoll makes the next poll of threadID check read positions.
func (c *TeamsClient) forgetReceiptPoll(threadID string) {
	c.receiptPollMu.Lock()
	defer c.receiptPollMu.Unlock()
	delete(c.receiptPoll, threadID)
}

func (c *TeamsClient) threadIsActive(threadID string, now time.Time) bool {
	c.receiptPollMu.Lock()
	defer c.receiptPollMu.Unlock()
	active, ok := c.threadActive[threadID]
	return ok && now.Sub(active) < threadActiveWindow
}

// idleCapFor is threadID's poll backoff idle cap.
func (c *TeamsClient) idleCapFor(threadID string, now time.Time) time.Duration {
	switch {
	case c.threadIsActive(threadID, now):
		return activeThreadIdleCap
	case c.longPollUp.Load() || c.activityUp.Load():
		return pollBackstopIdleCap
	default:
		return pollIdleCap
	}
}

// checkActivity lists the most recently active conversations and wakes each
// thread whose newest message changed since the previous check. lastSeen
// maps conversation ID to newest message ID between calls; the first call
// only fills it. unknown reports a changed conversation that matches no
// polled thread, i.e. a chat that discovery has not picked up yet.
func (c *TeamsClient) checkActivity(ctx context.Context, states map[string]*pollState, lastSeen map[string]string) (unknown bool, err error) {
	convs, err := c.getAPI().ListRecentConversations(ctx, c.skypeToken(), activityPageSize)
	c.noteTeamsResult(err)
	if err != nil {
		log := c.log()
		if c.activityUp.Swap(false) {
			log.Info().Bool("watching", false).Msg("Recent-conversations change watch state changed")
		}
		log.Debug().Err(err).Msg("Recent conversations check failed")
		return false, err
	}
	primed := len(lastSeen) > 0
	watched := false
	for _, conv := range convs {
		newest := conv.LastMessage.ID
		if conv.ID == "" || newest == "" {
			continue
		}
		watched = true
		if prev, ok := lastSeen[conv.ID]; prev != newest && (ok || primed) {
			w := pollWakeup{threadID: conv.ID, at: time.Now(), source: "watch"}
			if !c.applyWakeup(w, states) && !isNonPollableSystemStream(conv.ID) {
				unknown = true
			}
		}
		lastSeen[conv.ID] = newest
	}
	// A list without newest-message IDs cannot show changes; keep the
	// regular cadence then.
	if c.activityUp.Swap(watched) != watched {
		log := c.log()
		log.Info().Bool("watching", watched).Int("conversations", len(convs)).
			Msg("Recent-conversations change watch state changed")
	}
	return unknown, nil
}

// applyWakeup makes the threads matching w due now, at the fastest cadence.
// It reports whether any thread matched.
func (c *TeamsClient) applyWakeup(w pollWakeup, states map[string]*pollState) bool {
	matched := false
	for threadID, ps := range states {
		if threadID != w.threadID && ps.conversation != w.threadID {
			continue
		}
		matched = true
		ps.nextPoll = time.Time{}
		// The earliest notice since the last poll is when the wait began.
		if !w.at.IsZero() && (ps.noticedAt.IsZero() || w.at.Before(ps.noticedAt)) {
			ps.noticedAt, ps.noticedBy = w.at, w.source
		}
		ps.backoff.OnSuccess()
		if w.receipts {
			c.forgetReceiptPoll(threadID)
		}
	}
	return matched
}
