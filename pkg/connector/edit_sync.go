package connector

// In-place edits and deletes.
//
// Teams edits a message where it stands: the same message ID and sequence
// ID, new content, and a properties.edittime.  It deletes one the same way,
// emptying it and setting properties.deletetime.  The poller skips messages
// at or below its cursor, so neither arrives as a new message (only some
// clients also send an edit as one carrying skypeeditedid, or a delete as a
// MessageDelete, handled in queueUserMessage).  Every poll page carries the
// thread's recent messages again, though: an already-bridged one whose
// edittime is newer than the one recorded in its message metadata is
// queued as an edit, and an already-bridged one that is deleted is queued
// as a delete.  An in-memory map of each message's state as last checked
// keeps the database lookup to messages whose state changed since the last
// poll.

import (
	"context"
	"strconv"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

// queueInPlaceChange queues an edit or a delete for msg, a message the
// poller has already passed, when Teams has changed it since it was bridged.
func (c *TeamsClient) queueInPlaceChange(ctx context.Context, th *teamsdb.ThreadState, msg model.RemoteMessage, messageID, selfID string) {
	if model.IsDeleted(msg.PropertiesRaw) {
		c.queueInPlaceDelete(ctx, th, messageID)
		return
	}
	c.queueInPlaceEdit(ctx, th, msg, messageID, selfID)
}

// queueInPlaceDelete queues the removal of an already-bridged message that
// Teams now lists as deleted.
func (c *TeamsClient) queueInPlaceDelete(ctx context.Context, th *teamsdb.ThreadState, messageID string) {
	if c == nil || c.Login == nil || th == nil || messageID == "" {
		return
	}
	if !c.messageStateChanged(messageID, "deleted") {
		return
	}
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return
	}
	portalKey := c.portalKey(th.ThreadID)
	part, err := c.Main.Bridge.DB.Message.GetFirstPartByID(ctx, portalKey.Receiver, networkid.MessageID(messageID))
	if err != nil {
		log := c.log()
		log.Warn().Err(err).Str("message_id", messageID).Msg("Failed to look up deleted message")
		c.forgetMessageState(messageID)
		return
	}
	if part == nil {
		// Never bridged, or already removed.
		return
	}
	log := c.log()
	log.Debug().Str("thread_id", th.ThreadID).Str("message_id", messageID).Msg("Bridging in-place Teams delete")
	// No sender: Teams does not say who deleted it, and bridgev2 then
	// redacts as whoever sent the message (ghost or double puppet).
	c.queueRemoteEvent(&simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		TargetMessage: networkid.MessageID(messageID),
	})
}

// queueInPlaceEdit queues an edit for msg, a message the poller has
// already passed, when Teams has edited it since it was bridged.
func (c *TeamsClient) queueInPlaceEdit(ctx context.Context, th *teamsdb.ThreadState, msg model.RemoteMessage, messageID, selfID string) {
	if c == nil || c.Login == nil || th == nil || messageID == "" {
		return
	}
	editTime := model.ExtractEditTime(msg.PropertiesRaw)
	if editTime <= 0 || model.IsDeleted(msg.PropertiesRaw) {
		return
	}
	if !c.messageStateChanged(messageID, strconv.FormatInt(editTime, 10)) {
		return
	}
	senderID := model.NormalizeTeamsUserID(msg.SenderID)
	if senderID == "" || isLikelyThreadID(senderID) || isSystemMessageType(msg.MessageType) {
		return
	}
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return
	}
	portalKey := c.portalKey(th.ThreadID)
	parts, err := c.Main.Bridge.DB.Message.GetAllPartsByID(ctx, portalKey.Receiver, networkid.MessageID(messageID))
	if err != nil {
		log := c.log()
		log.Warn().Err(err).Str("message_id", messageID).Msg("Failed to look up edited message")
		c.forgetMessageState(messageID)
		return
	}
	if len(parts) == 0 {
		// Never bridged (or bridged under another ID): nothing to edit.
		return
	}
	for _, part := range parts {
		if meta, ok := part.Metadata.(*teamsid.MessageMetadata); ok && meta.EditTime >= editTime {
			return
		}
	}

	es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(senderID)}
	// bridgev2 only lets the original sender edit.  Own messages bridged
	// before double puppeting was set up came from the Teams ghost, so
	// their edits must come from it too, not from the double puppet.
	if selfID != "" && senderID == selfID && parts[0].SenderMXID == c.Login.UserMXID {
		es.IsFromMe = true
		es.SenderLogin = c.Login.ID
	}
	ts := time.UnixMilli(editTime)
	log := c.log()
	log.Debug().
		Str("thread_id", th.ThreadID).
		Str("message_id", messageID).
		Time("edit_time", ts).
		Msg("Bridging in-place Teams edit")
	c.queueRemoteEvent(&simplevent.Message[model.RemoteMessage]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventEdit,
			PortalKey: portalKey,
			Sender:    es,
			Timestamp: ts,
		},
		Data:            msg,
		ID:              networkid.MessageID(messageID + ":edit:" + strconv.FormatInt(editTime, 10)),
		TargetMessage:   networkid.MessageID(messageID),
		ConvertEditFunc: c.convertTeamsEdit,
	})
}

// messageStateChanged records sig (an edittime, or "deleted") as the last
// state seen for messageID and reports whether it differs from the
// previous one (or there was none).
func (c *TeamsClient) messageStateChanged(messageID, sig string) bool {
	c.editSeenMu.Lock()
	defer c.editSeenMu.Unlock()
	if c.editTimes == nil {
		c.editTimes = make(map[string]stampedSig)
	}
	prev, known := c.editTimes[messageID]
	c.editTimes[messageID] = stampedSig{sig: sig, at: time.Now()}
	return !known || prev.sig != sig
}

// forgetMessageState drops messageID's entry so the next poll checks it
// again.
func (c *TeamsClient) forgetMessageState(messageID string) {
	c.editSeenMu.Lock()
	delete(c.editTimes, messageID)
	c.editSeenMu.Unlock()
}
