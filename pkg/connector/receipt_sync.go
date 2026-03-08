package connector

// Consumption horizon polling and read receipt sync.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

const receiptPollInterval = 30 * time.Second

const getLastMessagePartBySenderAtOrBeforeTimeQuery = `
	SELECT id
	FROM message
	WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3 AND sender_id=$4 AND timestamp<=$5
	ORDER BY timestamp DESC, part_id DESC
	LIMIT 1
`

func (c *TeamsClient) pollConsumptionHorizons(ctx context.Context, th *teamsdb.ThreadState, now time.Time) error {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil || th == nil {
		return nil
	}
	threadID := strings.TrimSpace(th.ThreadID)
	if threadID == "" {
		return nil
	}
	log := zerolog.Ctx(ctx).With().Str("thread_id", threadID).Logger()
	if !c.shouldPollReceipts(threadID, now) {
		return nil
	}

	resp, err := c.getAPI().GetConsumptionHorizons(ctx, threadID)
	if err != nil || resp == nil {
		return err
	}

	selfID := model.NormalizeTeamsUserID(c.Meta.TeamsUserID)
	if selfID == "" {
		return nil
	}

	var remoteID string
	var remoteHorizon *model.ConsumptionHorizon
	nonSelfCount := 0
	for idx := range resp.Horizons {
		entry := &resp.Horizons[idx]
		entryID := model.NormalizeTeamsUserID(entry.ID)
		if entryID == "" || entryID == selfID || strings.EqualFold(entryID, threadID) || isLikelyThreadID(entryID) {
			continue
		}
		nonSelfCount++
		if nonSelfCount > 1 {
			return nil
		}
		remoteID = entryID
		remoteHorizon = entry
	}
	if remoteHorizon == nil || remoteID == "" {
		return nil
	}

	latestReadTS, ok := model.ParseConsumptionHorizonLatestReadTS(remoteHorizon.ConsumptionHorizon)
	if !ok || latestReadTS <= 0 {
		return nil
	}

	state, err := c.Main.DB.ConsumptionHorizon.Get(ctx, c.Login.ID, threadID, remoteID)
	if err != nil {
		return err
	}
	if state != nil && latestReadTS <= state.LastReadTS {
		return nil
	}
	log.Debug().
		Str("remote_user_id", remoteID).
		Int64("latest_read_ts", latestReadTS).
		Msg("consumption horizon advanced")

	readUpTo := time.UnixMilli(latestReadTS).UTC()
	portalKey := c.portalKey(threadID)
	targetID, err := c.getLastSentMessagePartAtOrBeforeTime(ctx, portalKey, teamsUserIDToNetworkUserID(selfID), readUpTo)
	if err != nil {
		return err
	}

	receipt := &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: portalKey,
			Sender: bridgev2.EventSender{
				Sender: teamsUserIDToNetworkUserID(remoteID),
			},
			Timestamp: readUpTo,
		},
		ReadUpTo: readUpTo,
	}
	if targetID != "" {
		receipt.LastTarget = targetID
		receipt.Targets = []networkid.MessageID{targetID}
	}
	c.queueRemoteEvent(receipt)

	return c.Main.DB.ConsumptionHorizon.UpsertLastRead(ctx, c.Login.ID, threadID, remoteID, latestReadTS)
}

func (c *TeamsClient) getLastSentMessagePartAtOrBeforeTime(ctx context.Context, portal networkid.PortalKey, senderID networkid.UserID, maxTS time.Time) (networkid.MessageID, error) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return "", nil
	}
	if senderID == "" {
		return "", nil
	}
	var msgID networkid.MessageID
	err := c.Main.Bridge.DB.QueryRow(
		ctx,
		getLastMessagePartBySenderAtOrBeforeTimeQuery,
		c.Main.Bridge.DB.BridgeID,
		portal.ID,
		portal.Receiver,
		senderID,
		maxTS.UnixNano(),
	).Scan(&msgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return msgID, nil
}

func (c *TeamsClient) shouldPollReceipts(threadID string, now time.Time) bool {
	c.receiptPollMu.Lock()
	defer c.receiptPollMu.Unlock()
	if c.receiptPoll == nil {
		c.receiptPoll = make(map[string]time.Time)
	}
	last := c.receiptPoll[threadID]
	if !last.IsZero() && now.Sub(last) < receiptPollInterval {
		return false
	}
	c.receiptPoll[threadID] = now
	return true
}
