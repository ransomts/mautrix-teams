package connector

// Inbound reaction sync: building and dispatching ReactionSync events.

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

func (c *TeamsClient) queueReactionSyncForMessage(ctx context.Context, th *teamsdb.ThreadState, msg model.RemoteMessage, messageID string) {
	if c == nil || c.Login == nil || th == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		messageID = c.effectiveRemoteMessageID(msg)
	}
	if messageID == "" {
		return
	}
	// Every poll page carries the same old messages again; only re-announce
	// a message's reactions when the set actually changed since we last saw
	// it. This check comes before resolving the target, which can take a
	// database lookup per candidate ID, as it rules out nearly every
	// message on nearly every poll. It is keyed by the ID as polled, which
	// is stable for a message across polls.
	if !c.reactionStateChanged(messageID, reactionSignature(msg.Reactions)) {
		return
	}
	messageID = resolveReactionTarget(c, ctx, th.ThreadID, messageID, msg)

	data, hasReactions := c.buildReactionSyncData(msg.Reactions)
	if !hasReactions && !c.shouldSendEmptyReactionSync(ctx, th.ThreadID, messageID) {
		return
	}
	if data == nil {
		data = &bridgev2.ReactionSyncData{Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{}, HasAllUsers: true}
	}
	evt := &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: c.portalKey(th.ThreadID),
			Timestamp: msg.Timestamp,
		},
		TargetMessage: networkid.MessageID(messageID),
		Reactions:     data,
	}
	c.queueRemoteEvent(evt)
}

func (c *TeamsClient) buildReactionSyncData(reactions []model.MessageReaction) (*bridgev2.ReactionSyncData, bool) {
	if len(reactions) == 0 {
		return nil, false
	}
	users := make(map[networkid.UserID]*bridgev2.ReactionSyncUser)
	selfID := model.NormalizeTeamsUserID("")
	if c != nil && c.Meta != nil {
		selfID = model.NormalizeTeamsUserID(c.selfTeamsUserID())
	}

	for _, reaction := range reactions {
		emotionKey := strings.TrimSpace(reaction.EmotionKey)
		if emotionKey == "" {
			continue
		}
		emoji, ok := MapEmotionKeyToEmoji(emotionKey)
		if !ok {
			continue
		}
		for _, user := range reaction.Users {
			userID := model.NormalizeTeamsUserID(user.MRI)
			if userID == "" || isLikelyThreadID(userID) {
				continue
			}
			sender := bridgev2.EventSender{
				Sender: teamsUserIDToNetworkUserID(userID),
			}
			if selfID != "" && userID == selfID {
				sender.IsFromMe = true
				if c != nil && c.Login != nil {
					sender.SenderLogin = c.Login.ID
				}
			}
			rsu := users[sender.Sender]
			if rsu == nil {
				rsu = &bridgev2.ReactionSyncUser{HasAllReactions: true}
				users[sender.Sender] = rsu
			}
			br := &bridgev2.BackfillReaction{
				Sender:  sender,
				EmojiID: networkid.EmojiID(emotionKey),
				Emoji:   emoji,
			}
			if user.TimeMS != 0 {
				br.Timestamp = time.UnixMilli(user.TimeMS).UTC()
			}
			rsu.Reactions = append(rsu.Reactions, br)
		}
	}
	if len(users) == 0 {
		return nil, false
	}
	return &bridgev2.ReactionSyncData{
		Users:       users,
		HasAllUsers: true,
	}, true
}

func (c *TeamsClient) shouldSendEmptyReactionSync(ctx context.Context, threadID string, messageID string) bool {
	if c == nil {
		return false
	}
	if c.markReactionSeen(messageID, false) {
		return true
	}
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return false
	}
	existing, err := c.Main.Bridge.DB.Reaction.GetAllToMessage(ctx, c.portalKey(threadID).Receiver, networkid.MessageID(messageID))
	if err != nil {
		return false
	}
	return len(existing) > 0
}

// reactionSignature is a canonical, order-independent rendering of a
// message's reaction set.
func reactionSignature(reactions []model.MessageReaction) string {
	if len(reactions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reactions))
	for _, reaction := range reactions {
		key := strings.TrimSpace(reaction.EmotionKey)
		for _, user := range reaction.Users {
			parts = append(parts, key+"|"+model.NormalizeTeamsUserID(user.MRI)+"|"+strconv.FormatInt(user.TimeMS, 10))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// reactionStateChanged records sig for messageID and reports whether it
// differs from the previously recorded one. The first sighting counts as a
// change so state is announced once after startup. Every check refreshes
// the entry's age, so only messages no longer polled expire.
func (c *TeamsClient) reactionStateChanged(messageID string, sig string) bool {
	c.reactionSeenMu.Lock()
	defer c.reactionSeenMu.Unlock()
	if c.reactionSigs == nil {
		c.reactionSigs = make(map[string]stampedSig)
	}
	prev, known := c.reactionSigs[messageID]
	c.reactionSigs[messageID] = stampedSig{sig: sig, at: time.Now()}
	return !known || prev.sig != sig
}

// markReactionSeen records (seen) or clears messageID as having announced
// reactions and reports whether it had. Without an entry the caller falls
// back to the bridge database, so an expired one only costs a lookup.
func (c *TeamsClient) markReactionSeen(messageID string, seen bool) bool {
	c.reactionSeenMu.Lock()
	defer c.reactionSeenMu.Unlock()
	if c.reactionSeen == nil {
		c.reactionSeen = make(map[string]time.Time)
	}
	_, exists := c.reactionSeen[messageID]
	if seen {
		c.reactionSeen[messageID] = time.Now()
	} else if exists {
		delete(c.reactionSeen, messageID)
	}
	return exists
}

// resolveReactionTarget is resolveReactionSyncTargetMessageID; tests swap
// it to count lookups.
var resolveReactionTarget = (*TeamsClient).resolveReactionSyncTargetMessageID

// resolveReactionSyncTargetMessageID returns the ID the bridge stored the
// message under, trying the forms an older bridge used too.
func (c *TeamsClient) resolveReactionSyncTargetMessageID(ctx context.Context, threadID string, messageID string, msg model.RemoteMessage) string {
	candidates := buildReactionTargetMessageIDCandidates(messageID, msg)
	if len(candidates) == 0 {
		return ""
	}
	if c == nil || c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil || c.Main.Bridge.DB.Message == nil {
		return candidates[0]
	}
	receiver := c.portalKey(threadID).Receiver
	for _, candidate := range candidates {
		target, err := c.Main.Bridge.DB.Message.GetLastPartByID(ctx, receiver, networkid.MessageID(candidate))
		if err == nil && target != nil {
			return candidate
		}
	}
	return candidates[0]
}

func buildReactionTargetMessageIDCandidates(messageID string, msg model.RemoteMessage) []string {
	candidates := make([]string, 0, 8)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}
	addCanonicalAndLegacy := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		normalized := NormalizeTeamsReactionMessageID(value)
		addCandidate(normalized)
		if normalized != "" {
			addCandidate("msg/" + normalized)
		}
	}

	// Prefer the selected ID first.
	addCanonicalAndLegacy(messageID)
	// Also try raw IDs from the payload, including clientmessageid for local echo targets.
	addCanonicalAndLegacy(msg.MessageID)
	addCanonicalAndLegacy(msg.SequenceID)
	addCanonicalAndLegacy(msg.ClientMessageID)

	return candidates
}
