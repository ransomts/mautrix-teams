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

	selfID := model.NormalizeTeamsUserID(c.selfTeamsUserID())
	if selfID == "" {
		return nil
	}

	// Group chats have one horizon per participant; bridge each of them.
	var lastErr error
	for _, remote := range remoteHorizons(resp, selfID, threadID) {
		if err := c.syncParticipantHorizon(ctx, log, threadID, selfID, remote.id, remote.horizon); err != nil {
			log.Debug().Err(err).Str("remote_user_id", remote.id).Msg("consumption horizon sync failed")
			lastErr = err
		}
	}
	if err := c.syncOwnHorizon(ctx, log, threadID, selfID, ownHorizon(resp, selfID)); err != nil {
		log.Debug().Err(err).Msg("own consumption horizon sync failed")
		lastErr = err
	}
	return lastErr
}

type remoteHorizon struct {
	id      string
	horizon *model.ConsumptionHorizon
}

// remoteHorizons returns the horizons of every participant other than the
// bridge user, ignoring thread pseudo-participants.
func remoteHorizons(resp *model.ConsumptionHorizonsResponse, selfID string, threadID string) []remoteHorizon {
	if resp == nil {
		return nil
	}
	out := make([]remoteHorizon, 0, len(resp.Horizons))
	for idx := range resp.Horizons {
		entry := &resp.Horizons[idx]
		entryID := model.NormalizeTeamsUserID(entry.ID)
		if entryID == "" || entryID == selfID || strings.EqualFold(entryID, threadID) || isLikelyThreadID(entryID) {
			continue
		}
		out = append(out, remoteHorizon{id: entryID, horizon: entry})
	}
	return out
}

// ownHorizon returns the bridge user's own horizon, or nil.
func ownHorizon(resp *model.ConsumptionHorizonsResponse, selfID string) *model.ConsumptionHorizon {
	if resp == nil {
		return nil
	}
	for idx := range resp.Horizons {
		if model.NormalizeTeamsUserID(resp.Horizons[idx].ID) == selfID {
			return &resp.Horizons[idx]
		}
	}
	return nil
}

// syncOwnHorizon marks the room read on Matrix up to where the user has
// read the chat in a Teams client.  bridgev2 sends it with the user's
// double puppet; without one there is no one to send it as, so it is
// dropped (and not recorded, so it is sent once double puppeting exists).
func (c *TeamsClient) syncOwnHorizon(ctx context.Context, log zerolog.Logger, threadID string, selfID string, horizon *model.ConsumptionHorizon) error {
	if horizon == nil || c.Login == nil || c.Login.User == nil || c.Login.User.DoublePuppet(ctx) == nil {
		return nil
	}
	latestReadTS, ok := model.ParseConsumptionHorizonLatestReadTS(horizon.ConsumptionHorizon)
	if !ok || latestReadTS <= 0 {
		return nil
	}
	state, err := c.Main.DB.ConsumptionHorizon.Get(ctx, c.Login.ID, threadID, selfID)
	if err != nil {
		return err
	}
	if state != nil && latestReadTS <= state.LastReadTS {
		return nil
	}
	log.Debug().Int64("latest_read_ts", latestReadTS).Msg("own consumption horizon advanced")
	readUpTo := time.UnixMilli(latestReadTS).UTC()
	// No target: bridgev2 marks the last message at or before readUpTo.
	c.queueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: c.portalKey(threadID),
			Sender:    bridgev2.EventSender{IsFromMe: true, SenderLogin: c.Login.ID, Sender: teamsUserIDToNetworkUserID(selfID)},
			Timestamp: readUpTo,
		},
		ReadUpTo: readUpTo,
	})
	return c.Main.DB.ConsumptionHorizon.UpsertLastRead(ctx, c.Login.ID, threadID, selfID, latestReadTS)
}

// ownHorizonCovers reports whether Teams already has the user's read
// position at or past readUpTo, as it does when the Matrix receipt is the
// echo of one syncOwnHorizon sent.
func (c *TeamsClient) ownHorizonCovers(ctx context.Context, threadID string, readUpTo time.Time) bool {
	selfID := model.NormalizeTeamsUserID(c.selfTeamsUserID())
	if readUpTo.IsZero() || selfID == "" || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return false
	}
	state, err := c.Main.DB.ConsumptionHorizon.Get(ctx, c.Login.ID, threadID, selfID)
	return err == nil && state != nil && readUpTo.UnixMilli() <= state.LastReadTS
}

// recordOwnHorizon stores the horizon the bridge just set on Teams, so the
// next poll does not send it back to Matrix.
func (c *TeamsClient) recordOwnHorizon(ctx context.Context, threadID string, ts time.Time) {
	selfID := model.NormalizeTeamsUserID(c.selfTeamsUserID())
	if selfID == "" || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return
	}
	_ = c.Main.DB.ConsumptionHorizon.UpsertLastRead(ctx, c.Login.ID, threadID, selfID, ts.UnixMilli())
}

func (c *TeamsClient) syncParticipantHorizon(ctx context.Context, log zerolog.Logger, threadID string, selfID string, remoteID string, remoteHorizon *model.ConsumptionHorizon) error {
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
	// A thread with a recent own send is checked on every poll, which
	// follows the thread's cadence; others every receiptIdlePollInterval.
	interval := receiptIdlePollInterval
	if active, ok := c.threadActive[threadID]; ok && now.Sub(active) < threadActiveWindow {
		interval = 0
	}
	last := c.receiptPoll[threadID]
	if !last.IsZero() && now.Sub(last) < interval {
		return false
	}
	c.receiptPoll[threadID] = now
	return true
}
