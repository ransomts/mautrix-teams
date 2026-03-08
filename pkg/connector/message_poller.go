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
	"maunium.net/go/mautrix/bridgev2/status"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

type pollState struct {
	backoff  PollBackoff
	nextPoll time.Time
}

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
	log := zerolog.Ctx(ctx)
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

	nextDiscovery := time.Now().UTC().Add(threadDiscoveryInterval)
	if !initialDiscoverySucceeded {
		nextDiscovery = time.Time{}
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := time.Now().UTC()
		if nextDiscovery.IsZero() || !now.Before(nextDiscovery) {
			if err := c.refreshThreads(ctx); err != nil {
				log.Err(err).Msg("Teams thread discovery refresh failed")
			}
			nextDiscovery = now.Add(threadDiscoveryInterval)
		}
		nextWake := now.Add(5 * time.Second)

		threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
		if err != nil {
			return err
		}
		for _, th := range threads {
			if th == nil || th.ThreadID == "" {
				continue
			}
			ps := states[th.ThreadID]
			if ps == nil {
				ps = &pollState{backoff: PollBackoff{Delay: pollBaseDelay}, nextPoll: now}
				states[th.ThreadID] = ps
			}
			if now.Before(ps.nextPoll) {
				if ps.nextPoll.Before(nextWake) {
					nextWake = ps.nextPoll
				}
				continue
			}

			ingested, err := c.pollThread(ctx, th, now)
			delay, _ := ApplyPollBackoff(&ps.backoff, ingested, err)
			ps.nextPoll = now.Add(delay)
			if ps.nextPoll.Before(nextWake) {
				nextWake = ps.nextPoll
			}
		}

		sleep := time.Until(nextWake)
		if sleep < 100*time.Millisecond {
			sleep = 100 * time.Millisecond
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *TeamsClient) pollThread(ctx context.Context, th *teamsdb.ThreadState, now time.Time) (int, error) {
	if c == nil || th == nil {
		return 0, nil
	}
	log := c.log()
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Message: err.Error(), UserAction: status.UserActionRelogin})
		return 0, err
	}
	log.Trace().Str("thread_id", th.ThreadID).Str("last_seq", th.LastSequenceID).Msg("Polling thread")
	msgs, err := c.getAPI().ListMessages(ctx, th.Conversation, th.LastSequenceID)
	if err != nil {
		log.Warn().Err(err).Str("thread_id", th.ThreadID).Msg("Failed to poll thread")
		return 0, err
	}

	lastSeq := strings.TrimSpace(th.LastSequenceID)
	var maxSeq string
	var maxTS int64
	ingested := 0
	selfID := ""
	if c.Meta != nil {
		selfID = model.NormalizeTeamsUserID(c.Meta.TeamsUserID)
	}

	for _, msg := range msgs {
		if strings.TrimSpace(msg.MessageID) == "" {
			continue
		}

		// Detect typing indicators and emit them as remote events.
		if strings.EqualFold(msg.MessageType, "Control/Typing") || strings.EqualFold(msg.MessageType, "Control/ClearTyping") ||
			strings.EqualFold(msg.MessageType, "Control/LiveState") {
			senderID := model.NormalizeTeamsUserID(msg.SenderID)
			if senderID != "" && senderID != selfID && !isLikelyThreadID(senderID) {
				timeout := 5 * time.Second
				if strings.EqualFold(msg.MessageType, "Control/ClearTyping") {
					timeout = 0
				}
				if c.shouldEmitTyping(th.ThreadID, senderID) {
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
			}
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
		if senderID == "" || strings.EqualFold(senderID, strings.TrimSpace(th.ThreadID)) || isLikelyThreadID(senderID) {
			zerolog.Ctx(ctx).Debug().
				Str("thread_id", th.ThreadID).
				Str("message_id", msg.MessageID).
				Str("sender_id", senderID).
				Msg("Skipping Teams message with non-user sender ID")
			continue
		}

		displayName := strings.TrimSpace(msg.IMDisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(msg.TokenDisplayName)
		}
		if displayName == "" {
			displayName = senderID
		}
		_ = c.Main.DB.Profile.Upsert(ctx, senderID, displayName, now)
		msg.SenderName = displayName

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
			c.queueRemoteEvent(deleteEvt)
			continue
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
			c.queueRemoteEvent(editEvt)
			c.queueReactionSyncForMessage(ctx, th, msg, eventMessageID)
			continue
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
		c.queueRemoteEvent(evt)
		c.queueReactionSyncForMessage(ctx, th, msg, eventMessageID)
		ingested++
		// Preserve send-intent echo reconciliation for message ID mapping while
		// keeping unread state unchanged for self-sent events.
		if isSelfEcho {
			continue
		}
		if !es.IsFromMe && strings.TrimSpace(th.ThreadID) != "" {
			c.markUnread(th.ThreadID)
		}
	}

	if maxSeq != "" {
		_ = c.Main.DB.ThreadState.UpdateCursor(ctx, c.Login.ID, th.ThreadID, maxSeq, maxTS)
		th.LastSequenceID = maxSeq
		th.LastMessageTS = maxTS
	}
	if ingested > 0 {
		log.Debug().Str("thread_id", th.ThreadID).Int("ingested", ingested).Str("max_seq", maxSeq).Msg("Ingested new messages from thread")
	}
	_ = c.pollConsumptionHorizons(ctx, th, now)
	return ingested, nil
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
	if lastSeen, ok := c.typingSeen[key]; ok && now.Sub(lastSeen) < 3*time.Second {
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
func (c *TeamsClient) queueRemoteEvent(evt bridgev2.RemoteEvent) {
	if c.events != nil {
		c.events.QueueRemoteEvent(evt)
	} else if c.Login != nil {
		c.Login.QueueRemoteEvent(evt)
	}
}
