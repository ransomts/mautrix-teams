package connector

// Thread message polling and event dispatch.

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

type pollState struct {
	backoff      PollBackoff
	nextPoll     time.Time
	conversation string // the thread's conversation ID, for matching wakeups
	gone         bool   // Teams reported the thread deleted; polled rarely
	// noticedAt and noticedBy record the first wakeup since the last poll,
	// for the latency log (message_latency.go).
	noticedAt time.Time
	noticedBy string
}

const (
	// pollLoopMaxSleep bounds how long the poll loop sleeps between passes.
	pollLoopMaxSleep = 5 * time.Second
	// pollLoopMinSleep prevents a hot loop when many threads are due at once.
	pollLoopMinSleep = 100 * time.Millisecond
	// typingTimeout is how long a bridged typing indicator stays active.
	typingTimeout = 5 * time.Second
	// typingMaxAge drops typing control messages older than this; they are
	// stale leftovers from a previous poll page, not live typing.
	typingMaxAge = 15 * time.Second
	// typingDedupWindow suppresses repeated typing events for one sender.
	typingDedupWindow = 3 * time.Second
)

func (c *TeamsClient) pollAllThreadsOnce(ctx context.Context) error {
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return err
	}
	for _, th := range threads {
		_, _ = c.pollThread(ctx, th, time.Now().UTC())
	}
	return nil
}

func (c *TeamsClient) pollDueThreads(ctx context.Context, initialDiscoverySucceeded bool) error {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return nil
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return err
	}

	states := make(map[string]*pollState, len(threads))
	for _, th := range threads {
		if th == nil || th.ThreadID == "" {
			continue
		}
		states[th.ThreadID] = &pollState{backoff: PollBackoff{Delay: pollBaseDelay}, nextPoll: time.Now().UTC()}
	}

	sched := &pollSchedule{lastSeen: make(map[string]string)}
	if initialDiscoverySucceeded {
		sched.nextDiscovery = time.Now().UTC().Add(c.discoveryInterval())
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.superseded() {
			return errClientSuperseded
		}
		now := time.Now().UTC()
		nextWake := sched.throttledUntil
		if !now.Before(sched.throttledUntil) {
			if nextWake, err = c.pollPass(ctx, now, states, sched); err != nil {
				return err
			}
		}

		sleep := time.Until(nextWake)
		if sleep < pollLoopMinSleep {
			sleep = pollLoopMinSleep
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		case w := <-c.wakeChan():
			timer.Stop()
			c.applyWakeup(w, states)
			// Take every queued wakeup before the next pass.
			for drained := false; !drained; {
				select {
				case w := <-c.wakeChan():
					c.applyWakeup(w, states)
				default:
					drained = true
				}
			}
		}
	}
}

// pollSchedule is the poll loop's timing state between passes.
type pollSchedule struct {
	nextDiscovery, lastDiscovery time.Time
	nextActivity                 time.Time
	activityFailures             int
	// nextSweep is when the client's caches are next swept.
	nextSweep time.Time
	// nextStats is when the request tally is next logged.
	nextStats time.Time
	// throttledUntil pauses all polling after Teams answers 429.
	throttledUntil time.Time
	// lastSeen maps conversation ID to its newest message ID, for the
	// recent-conversations check.
	lastSeen map[string]string
}

// throttle pauses all polling if err is a 429 and reports whether it was.
func (s *pollSchedule) throttle(ctx context.Context, err error, now time.Time) bool {
	d, ok := teamsThrottle(err)
	if !ok {
		return false
	}
	if until := now.Add(d); until.After(s.throttledUntil) {
		s.throttledUntil = until
		zerolog.Ctx(ctx).Warn().Dur("pause", d).Msg("Teams is rate-limiting the bridge, pausing polling")
	}
	return true
}

// pollPass runs whatever is due at now: discovery, the recent-conversations
// check, and every thread whose next poll has come. It returns when the loop
// should wake next.
func (c *TeamsClient) pollPass(ctx context.Context, now time.Time, states map[string]*pollState, sched *pollSchedule) (time.Time, error) {
	log := zerolog.Ctx(ctx)
	if sched.nextDiscovery.IsZero() || !now.Before(sched.nextDiscovery) {
		if err := c.refreshThreads(ctx); err != nil && !sched.throttle(ctx, err, now) {
			log.Err(err).Msg("Teams thread discovery refresh failed")
		}
		sched.lastDiscovery = now
		sched.nextDiscovery = now.Add(c.discoveryInterval())
	}
	if !now.Before(sched.nextActivity) {
		c.runActivityCheck(ctx, now, states, sched)
	}
	if now.Before(sched.throttledUntil) {
		return sched.throttledUntil, nil
	}
	// Both are always set by now; one already due (discovery pulled forward
	// for a new chat) makes the next pass come at once.
	nextWake := now.Add(pollLoopMaxSleep)
	for _, t := range []time.Time{sched.nextActivity, sched.nextDiscovery} {
		if t.Before(nextWake) {
			nextWake = t
		}
	}

	if !now.Before(sched.nextSweep) {
		if n := c.sweepCaches(now); n > 0 {
			log.Debug().Int("removed", n).Msg("Swept expired cache entries")
		}
		sched.nextSweep = now.Add(cacheSweepInterval)
	}
	if sched.nextStats.IsZero() {
		auth.TakeRequestStats() // start the first hour's tally now
		sched.nextStats = now.Add(requestStatsInterval)
	} else if !now.Before(sched.nextStats) {
		logRequestStats(*log, now.Sub(sched.nextStats.Add(-requestStatsInterval)))
		sched.nextStats = now.Add(requestStatsInterval)
	}

	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return nextWake, err
	}
	listed := make(map[string]struct{}, len(threads))
	for _, th := range threads {
		if th != nil && th.ThreadID != "" {
			listed[th.ThreadID] = struct{}{}
		}
	}
	c.forgetRemovedThreads(states, listed)
	for _, th := range threads {
		if th == nil || th.ThreadID == "" {
			continue
		}
		ps := states[th.ThreadID]
		if ps == nil {
			ps = &pollState{backoff: PollBackoff{Delay: pollBaseDelay}, nextPoll: now}
			states[th.ThreadID] = ps
		}
		ps.conversation = th.Conversation
		if now.Before(ps.nextPoll) {
			if ps.nextPoll.Before(nextWake) {
				nextWake = ps.nextPoll
			}
			continue
		}

		trigger := &pollTrigger{noticed: ps.noticedAt, by: ps.noticedBy}
		if trigger.noticed.IsZero() {
			trigger.noticed, trigger.by = now, "schedule"
		}
		ps.noticedAt, ps.noticedBy = time.Time{}, ""
		ingested, err := c.pollThread(withPollTrigger(ctx, trigger), th, now)
		if sched.throttle(ctx, err, now) {
			// Leave this thread and the rest due; they go first once the
			// pause is over.
			return sched.throttledUntil, nil
		}
		if isThreadGone(err) {
			if !ps.gone {
				ps.gone = true
				log.Info().Str("thread_id", th.ThreadID).Dur("recheck", goneThreadRecheck).
					Msg("Teams reports the thread deleted, polling it rarely")
			}
			ps.nextPoll = now.Add(goneThreadRecheck)
			continue
		}
		ps.gone = false
		ps.backoff.IdleCap = c.idleCapFor(th.ThreadID, now)
		delay, reason := ApplyPollBackoff(&ps.backoff, ingested, err)
		if reason == PollBackoffIdle && ps.backoff.IdleCap == pollBackstopIdleCap {
			// Changes are watched: an idle thread needs no gradual ramp.
			ps.backoff.Delay = pollBackstopIdleCap
			delay = pollBackstopIdleCap
		}
		ps.nextPoll = now.Add(delay)
		if ps.nextPoll.Before(nextWake) {
			nextWake = ps.nextPoll
		}
	}
	return nextWake, nil
}

// runActivityCheck runs the recent-conversations check with a fresh token,
// spacing it out while it fails, and schedules discovery when it sees a
// conversation the poller does not know.
func (c *TeamsClient) runActivityCheck(ctx context.Context, now time.Time, states map[string]*pollState, sched *pollSchedule) {
	err := c.ensureValidSkypeToken(ctx)
	if err != nil {
		c.reportTokenError(err)
	} else {
		var unknown bool
		unknown, err = c.checkActivity(ctx, states, sched.lastSeen)
		// A new chat: find it now rather than at the next scheduled
		// discovery, but not more than once per gap, so a conversation
		// discovery never adds cannot make it run on every check.
		if unknown && now.Sub(sched.lastDiscovery) >= discoveryMinGap {
			sched.nextDiscovery = now
		}
	}
	if err != nil {
		sched.throttle(ctx, err, now)
		sched.activityFailures++
	} else {
		sched.activityFailures = 0
	}
	sched.nextActivity = now.Add(activityRetryDelay(sched.activityFailures))
}

// pollFailureLevel is the level to log a failed thread poll at.  A deleted
// thread fails every time, and during an outage every thread does; the poll
// loop and the bridge state already say so.  A poll cut short because the
// client is stopping (a re-login replaced it, or shutdown) is no failure.
func (c *TeamsClient) pollFailureLevel(ctx context.Context, err error) zerolog.Level {
	if loopStopped(err) || ctx.Err() != nil || isThreadGone(err) || c.reach.isDown() {
		return zerolog.DebugLevel
	}
	return zerolog.WarnLevel
}

func (c *TeamsClient) pollThread(ctx context.Context, th *teamsdb.ThreadState, now time.Time) (int, error) {
	if c == nil || th == nil {
		return 0, nil
	}
	// Teams system streams (drafts, annotations, notifications, mentions,
	// threads) are not conversations: their entries are bookkeeping, and they
	// once rejected polling with HTTP 400 "Invalid threadId" (all answer 200
	// as of 2026-09).  Skip them.  "notes" is a real chat, and the call log
	// carries one entry per call (see call_log.go), so both poll normally.
	if isNonPollableSystemStream(th.ThreadID) || isNonPollableSystemStream(th.Conversation) {
		return 0, nil
	}
	log := c.log()
	msgs, err := c.fetchThreadPage(ctx, th)
	if err != nil {
		return 0, err
	}

	lastSeq := strings.TrimSpace(th.LastSequenceID)
	var maxSeq string
	var maxTS int64
	ingested := 0
	selfID := model.NormalizeTeamsUserID(c.selfTeamsUserID())
	// The cursor moves past this page only once bridgev2 has handled the
	// events that advance it; see cursor_commit.go.
	commit := newCursorCommit()

	for _, msg := range msgs {
		if strings.TrimSpace(msg.MessageID) == "" {
			continue
		}

		if strings.EqualFold(msg.MessageType, "Control/Typing") || strings.EqualFold(msg.MessageType, "Control/ClearTyping") ||
			strings.EqualFold(msg.MessageType, "Control/LiveState") {
			c.handleTypingControl(th, msg, lastSeq, selfID, now)
			continue
		}

		effectiveMessageID := c.effectiveRemoteMessageID(msg)
		// Filter already-seen messages in case the remote API returns history.
		if lastSeq != "" && model.CompareSequenceID(strings.TrimSpace(msg.SequenceID), lastSeq) <= 0 {
			// Still process reactions on older messages for sync parity.
			c.queueReactionSyncForMessage(ctx, th, msg, effectiveMessageID)
			continue
		}
		if maxSeq == "" || model.CompareSequenceID(strings.TrimSpace(msg.SequenceID), maxSeq) > 0 {
			maxSeq = strings.TrimSpace(msg.SequenceID)
		}
		if ts := msg.Timestamp.UnixMilli(); ts > maxTS {
			maxTS = ts
		}

		senderID := model.NormalizeTeamsUserID(msg.SenderID)
		if senderID == "" || strings.EqualFold(senderID, strings.TrimSpace(th.ThreadID)) || isLikelyThreadID(senderID) ||
			isSystemMessageType(msg.MessageType) {
			if c.queueNonUserMessage(ctx, th, msg, senderID, commit) {
				ingested++
			}
			continue
		}

		msg.SenderName = c.resolveSenderName(ctx, senderID, msg, now)
		if c.queueUserMessage(ctx, th, msg, senderID, selfID, effectiveMessageID, commit) {
			ingested++
		}
	}

	c.advancePageCursor(ctx, th, commit, maxSeq, maxTS)
	if ingested > 0 {
		log.Debug().Str("thread_id", th.ThreadID).Int("ingested", ingested).Str("max_seq", maxSeq).Msg("Ingested new messages from thread")
	}
	_ = c.pollConsumptionHorizons(ctx, th, now)
	return ingested, nil
}

// fetchThreadPage makes sure the Skype token is valid and fetches the
// thread's messages after its cursor, carrying the cursor past any page
// queued earlier but not yet handled.  A failed fetch is logged (at the
// level pollFailureLevel picks) and returned.
func (c *TeamsClient) fetchThreadPage(ctx context.Context, th *teamsdb.ThreadState) ([]model.RemoteMessage, error) {
	log := c.log()
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		c.reportTokenError(err)
		return nil, err
	}
	// A page queued earlier may not be handled (and so not saved) yet:
	// carry on after it rather than queue it again.
	if pos, ok := c.cursors.pending(th.ThreadID); ok && pos.newerThan(strings.TrimSpace(th.LastSequenceID)) {
		th.LastSequenceID, th.LastMessageTS = pos.seq, pos.ts
	}
	log.Trace().Str("thread_id", th.ThreadID).Str("last_seq", th.LastSequenceID).Msg("Polling thread")
	msgs, err := c.getAPI().ListMessages(ctx, th.Conversation, th.LastSequenceID)
	c.noteTeamsResult(err)
	if trigger := pollTriggerFrom(ctx); trigger != nil {
		trigger.fetched = time.Now()
	}
	if err != nil {
		log.WithLevel(c.pollFailureLevel(ctx, err)).Err(err).Str("thread_id", th.ThreadID).Msg("Failed to poll thread")
		return nil, err
	}
	return msgs, nil
}

// handleTypingControl emits a typing (or typing-stopped) event for a Teams
// typing control message.  Control messages never advance the cursor, so
// the same page can carry them on every poll: only ones newer than the
// cursor and recent are acted on, and never the user's own.
func (c *TeamsClient) handleTypingControl(th *teamsdb.ThreadState, msg model.RemoteMessage, lastSeq, selfID string, now time.Time) {
	if lastSeq != "" && model.CompareSequenceID(strings.TrimSpace(msg.SequenceID), lastSeq) <= 0 {
		return
	}
	if !msg.Timestamp.IsZero() && now.Sub(msg.Timestamp) > typingMaxAge {
		return
	}
	senderID := model.NormalizeTeamsUserID(msg.SenderID)
	if senderID == "" || senderID == selfID || isLikelyThreadID(senderID) {
		return
	}
	timeout := typingTimeout
	if strings.EqualFold(msg.MessageType, "Control/ClearTyping") {
		timeout = 0
	}
	if !c.shouldEmitTyping(th.ThreadID, senderID) {
		return
	}
	es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(senderID)}
	typingEvt := &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: c.portalKey(th.ThreadID),
			Sender:    es,
			Timestamp: msg.Timestamp,
		},
		Timeout: timeout,
		Type:    bridgev2.TypingTypeText,
	}
	c.queueRemoteEvent(typingEvt)
}

// queueNonUserMessage handles a message with no user sender (Teams system
// bookkeeping, or a thread ID as sender).  Teams' own record of a
// membership, role or name change is worth a line and is queued as a
// notice; its other bookkeeping is skipped.  It reports whether a notice
// was queued.
func (c *TeamsClient) queueNonUserMessage(ctx context.Context, th *teamsdb.ThreadState, msg model.RemoteMessage, senderID string, commit *cursorCommit) bool {
	if evt := c.systemMessageEvent(th, msg); evt != nil {
		c.queueForCursor(ctx, evt, &evt.EventMeta, commit)
		return true
	}
	zerolog.Ctx(ctx).Debug().
		Str("thread_id", th.ThreadID).
		Str("message_id", msg.MessageID).
		Str("sender_id", senderID).
		Str("message_type", msg.MessageType).
		Msg("Skipping Teams system message or non-user sender")
	return false
}

// resolveSenderName returns the display name to bridge a message under.  A
// name the message carries is saved to the profile table and pushed to the
// ghost; a message that carries none falls back to the saved profile, then
// to the Teams user ID, and never overwrites a known name.  The sender is
// also recorded as a known user.
func (c *TeamsClient) resolveSenderName(ctx context.Context, senderID string, msg model.RemoteMessage, now time.Time) string {
	displayName := strings.TrimSpace(msg.IMDisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(msg.TokenDisplayName)
	}
	if displayName != "" {
		if err := c.Main.DB.Profile.Upsert(ctx, senderID, displayName, now); err != nil {
			log := c.log()
			log.Warn().Err(err).Str("sender_id", senderID).Msg("Failed to save sender profile")
		}
		c.syncGhostName(ctx, senderID, displayName)
	} else if profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, senderID); err == nil && profile != nil && strings.TrimSpace(profile.DisplayName) != "" {
		// A message that carries no name must not overwrite a known one.
		displayName = strings.TrimSpace(profile.DisplayName)
	} else {
		displayName = senderID
	}
	c.trackKnownUser(senderID)
	return displayName
}

// queueUserMessage queues the remote event for a message from a real user:
// a delete, an edit, or a new message (plus its reaction sync), each tied
// to the page's cursor commit.  New messages from others mark the thread
// unread.  It reports whether a new message was queued, which is what
// counts as ingested.
func (c *TeamsClient) queueUserMessage(ctx context.Context, th *teamsdb.ThreadState, msg model.RemoteMessage, senderID, selfID, effectiveMessageID string, commit *cursorCommit) bool {
	es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(senderID)}
	if senderID != "" && selfID != "" && senderID == selfID {
		es.IsFromMe = true
		es.SenderLogin = c.Login.ID
	}
	if effectiveMessageID != "" && len(msg.Reactions) > 0 {
		c.markReactionSeen(effectiveMessageID, true)
	}

	clientMessageID := strings.TrimSpace(msg.ClientMessageID)
	isSelfEcho := senderID != "" && selfID != "" && senderID == selfID && c.consumeSelfMessage(clientMessageID)

	eventMessageID := effectiveMessageID
	if eventMessageID == "" {
		eventMessageID = strings.TrimSpace(msg.MessageID)
	}

	// Detect message deletes: messagetype contains "MessageDelete" or body is empty with delete properties.
	if strings.Contains(msg.MessageType, "MessageDelete") {
		deleteEvt := &simplevent.MessageRemove{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessageRemove,
				PortalKey: c.portalKey(th.ThreadID),
				Sender:    es,
				Timestamp: msg.Timestamp,
			},
			TargetMessage: networkid.MessageID(eventMessageID),
		}
		c.queueForCursor(ctx, deleteEvt, &deleteEvt.EventMeta, commit)
		return false
	}

	// Detect message edits: SkypeEditedID is non-empty.
	if msg.SkypeEditedID != "" {
		editEvt := &simplevent.Message[model.RemoteMessage]{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventEdit,
				PortalKey: c.portalKey(th.ThreadID),
				Sender:    es,
				Timestamp: msg.Timestamp,
			},
			Data:            msg,
			ID:              networkid.MessageID(eventMessageID),
			TargetMessage:   networkid.MessageID(strings.TrimSpace(msg.SkypeEditedID)),
			ConvertEditFunc: c.convertTeamsEdit,
		}
		c.queueForCursor(ctx, editEvt, &editEvt.EventMeta, commit)
		c.queueReactionSyncForMessage(ctx, th, msg, eventMessageID)
		return false
	}

	evt := &simplevent.Message[model.RemoteMessage]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    c.portalKey(th.ThreadID),
			Sender:       es,
			CreatePortal: true,
			Timestamp:    msg.Timestamp,
			StreamOrder:  msg.Timestamp.UnixMilli(),
		},
		Data:               msg,
		ID:                 networkid.MessageID(eventMessageID),
		TransactionID:      networkid.TransactionID(clientMessageID),
		ConvertMessageFunc: c.convertTeamsMessage,
	}
	if !es.IsFromMe {
		evt.PostHandleFunc = c.latencyLogger(ctx, th.ThreadID, eventMessageID, msg.Timestamp)
	}
	c.queueForCursor(ctx, evt, &evt.EventMeta, commit)
	c.queueReactionSyncForMessage(ctx, th, msg, eventMessageID)
	// Preserve send-intent echo reconciliation for message ID mapping while
	// keeping unread state unchanged for self-sent events.
	if !isSelfEcho && !es.IsFromMe && strings.TrimSpace(th.ThreadID) != "" {
		c.markUnread(th.ThreadID)
	}
	return true
}

// advancePageCursor moves the thread's cursor past the page just queued:
// in memory at once (so the next poll carries on after it), and in the
// database once bridgev2 has handled the events that advance it.
func (c *TeamsClient) advancePageCursor(ctx context.Context, th *teamsdb.ThreadState, commit *cursorCommit, maxSeq string, maxTS int64) {
	if maxSeq == "" {
		return
	}
	pos := cursorPos{seq: maxSeq, ts: maxTS}
	threadID := th.ThreadID
	c.cursors.noteQueued(threadID, pos)
	commit.release(ctx, func(ctx context.Context) { c.saveCursor(ctx, threadID, pos) })
	th.LastSequenceID = maxSeq
	th.LastMessageTS = maxTS
}

// isNonPollableSystemStream reports whether id is a Teams internal pseudo-stream
// that cannot be polled for messages. "teamsstream_notes" (the self-chat) is a
// real conversation and is excluded.
func isNonPollableSystemStream(id string) bool {
	return strings.Contains(id, "teamsstream_") &&
		!strings.Contains(id, "teamsstream_notes") &&
		!strings.Contains(id, "teamsstream_calllogs")
}

// shouldEmitTyping returns true if a typing event should be emitted for this
// thread+sender pair, deduplicating within a 3-second window.
func (c *TeamsClient) shouldEmitTyping(threadID, senderID string) bool {
	key := threadID + ":" + senderID
	now := time.Now().UTC()
	c.typingSeenMu.Lock()
	defer c.typingSeenMu.Unlock()
	if c.typingSeen == nil {
		c.typingSeen = make(map[string]time.Time)
	}
	if lastSeen, ok := c.typingSeen[key]; ok && now.Sub(lastSeen) < typingDedupWindow {
		return false
	}
	c.typingSeen[key] = now
	return true
}

func (c *TeamsClient) effectiveRemoteMessageID(msg model.RemoteMessage) string {
	effectiveMessageID := NormalizeTeamsReactionMessageID(msg.MessageID)
	if effectiveMessageID == "" {
		effectiveMessageID = NormalizeTeamsReactionMessageID(msg.SequenceID)
	}
	return effectiveMessageID
}

// queueRemoteEvent sends an event through the EventSink, falling back to the
// UserLogin when the sink has not been initialised yet.
func (c *TeamsClient) queueRemoteEvent(evt bridgev2.RemoteEvent) bridgev2.EventHandlingResult {
	if c.events != nil {
		return c.events.QueueRemoteEvent(evt)
	} else if c.Login != nil {
		return c.Login.QueueRemoteEvent(evt)
	}
	return bridgev2.EventHandlingResultIgnored
}
