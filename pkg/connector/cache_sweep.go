package connector

// Expiring the client's in-memory dedup and pacing maps.
//
// Each of these maps gains an entry per message, sender or thread and never
// had one removed, so a long-running bridge grew them without bound. The
// poll loop sweeps them every cacheSweepInterval. Every TTL is at least as
// long as the entry can still make a difference: an expired entry is treated
// exactly like a missing one would have been at that point, so sweeping
// changes memory use, not behaviour.

import (
	"time"
)

const (
	// cacheSweepInterval spaces the sweeps; entries a little past their
	// TTL cost nothing but memory.
	cacheSweepInterval = 10 * time.Minute
	// reactionStateTTL expires reaction signatures (and the "reactions were
	// announced" marks) of messages no poll has returned for this long.
	// Each poll that returns a message refreshes its entry, and a live
	// thread is polled at least every pollBackstopIdleCap (15 min), or after
	// a rate-limit pause of at most throttleMax; a parked thread
	// (goneThreadRecheck, 6 h) returns no messages at all. Only messages
	// that have left every poll page expire. Were one to come back, its
	// reactions would be announced once more, which bridgev2 applies as no
	// change; a missing "announced" mark falls back to the bridge database.
	reactionStateTTL = 12 * time.Hour
	// chatInfoTTL expires the chat-info signatures of threads discovery has
	// not listed for this long. Discovery lists every live thread at least
	// every discoveryIntervalWatched (5 min), refreshing its entry, so only
	// chats that have gone from the list (left, deleted) expire. One that
	// returns is announced once, as after a restart.
	chatInfoTTL = 12 * time.Hour
)

// stampedSig is a signature with the time it was last checked.
type stampedSig struct {
	sig string
	at  time.Time
}

func (s stampedSig) seenAt() time.Time { return s.at }

// timeOf is the identity, for maps whose values are the entries' times.
func timeOf(t time.Time) time.Time { return t }

// sweepBefore deletes the entries of m whose time, per at, is before cutoff
// and returns how many it deleted.
func sweepBefore[K comparable, V any](m map[K]V, cutoff time.Time, at func(V) time.Time) int {
	removed := 0
	for k, v := range m {
		if at(v).Before(cutoff) {
			delete(m, k)
			removed++
		}
	}
	return removed
}

// sweepCaches drops the entries of the client's maps that can no longer
// change anything at now, and reports how many went.
func (c *TeamsClient) sweepCaches(now time.Time) int {
	removed := 0

	c.reactionSeenMu.Lock()
	removed += sweepBefore(c.reactionSigs, now.Add(-reactionStateTTL), stampedSig.seenAt)
	removed += sweepBefore(c.reactionSeen, now.Add(-reactionStateTTL), timeOf)
	c.reactionSeenMu.Unlock()

	c.chatInfoMu.Lock()
	removed += sweepBefore(c.chatInfoSigs, now.Add(-chatInfoTTL), stampedSig.seenAt)
	c.chatInfoMu.Unlock()

	// Typing is only deduplicated within typingDedupWindow; an older entry
	// lets the next typing event through, as no entry would.
	c.typingSeenMu.Lock()
	removed += sweepBefore(c.typingSeen, now.Add(-typingDedupWindow), timeOf)
	c.typingSeenMu.Unlock()

	c.receiptPollMu.Lock()
	// A thread counts as active for threadActiveWindow after an own send.
	removed += sweepBefore(c.threadActive, now.Add(-threadActiveWindow), timeOf)
	// A read-position check older than receiptIdlePollInterval (the longest
	// spacing) makes the next one due, as no entry would.
	removed += sweepBefore(c.receiptPoll, now.Add(-receiptIdlePollInterval), timeOf)
	c.receiptPollMu.Unlock()

	// A Graph miss is only trusted for nameLookupMissTTL.
	c.nameLookupMu.Lock()
	removed += sweepBefore(c.nameLookupMiss, now.Add(-nameLookupMissTTL), timeOf)
	c.nameLookupMu.Unlock()

	return removed
}

// forgetRemovedThreads drops the poll state of threads that are no longer
// in the thread list, so the poller's maps follow the database.
func (c *TeamsClient) forgetRemovedThreads(states map[string]*pollState, listed map[string]struct{}) {
	for threadID := range states {
		if _, ok := listed[threadID]; !ok {
			delete(states, threadID)
			c.cursors.forget(threadID)
		}
	}
}
