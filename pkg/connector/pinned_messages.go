package connector

// Pinned messages in chats, both ways.
//
// Teams -> Matrix: Teams keeps a chat's pinned messages in the thread
// property pinnedItems, a JSON string listing them newest first.  The
// conversation list discovery walks leaves it out, so it is read from the
// thread itself (refreshPinnedMessages): when the poller sees the
// ThreadActivity/PinnedItemsUpdate message Teams posts on every pin and
// unpin (which says nothing of which message, and is not bridged), and for
// every chat at connect and hourly, with the team sync.  When it changed,
// the room's m.room.pinned_events is set by the bridge bot to the Matrix
// events of those messages (the ones bridged; older ones are left out).
//
// Matrix -> Teams: bridgev2 has no pin interface, so the connector adds its
// own handler for m.room.pinned_events to the appservice event processor
// (handleMatrixPinnedEvents).  A change by the user (not the bot or a
// ghost) is written to Teams' pinnedItems: messages newly pinned go first,
// as Teams puts them.  Portals let members pin (m.room.pinned_events at
// power level 0, ensurePinnable); bridgev2's default needs 50.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// pinnedItemsAPI is the part of the Teams client that writes a chat's
// pinned messages; separate from TeamsAPI so test fakes need not have it.
type pinnedItemsAPI interface {
	GetThreadPinnedItems(ctx context.Context, threadID string) (string, error)
	SetPinnedItems(ctx context.Context, threadID string, messageIDs []string) error
}

// pinnedItemsUpdateType is the message Teams posts on every pin and unpin.
const pinnedItemsUpdateType = "ThreadActivity/PinnedItemsUpdate"

// refreshPinnedMessages reads threadID's pinned messages from Teams and
// brings its room's pinned events up to date.
func (c *TeamsClient) refreshPinnedMessages(ctx context.Context, threadID string) {
	api, ok := c.getAPI().(pinnedItemsAPI)
	if !ok {
		return
	}
	raw, err := api.GetThreadPinnedItems(ctx, threadID)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Str("thread_id", threadID).Msg("Failed to read pinned messages")
		return
	}
	c.syncPinnedMessages(ctx, threadID, raw)
}

// refreshAllPinnedMessages refreshes the pinned messages of every chat
// with a room.  At connect and hourly; pins between come by
// pinnedItemsUpdateType.
func (c *TeamsClient) refreshAllPinnedMessages(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Main.Bridge == nil || c.Login == nil {
		return
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return
	}
	for _, th := range threads {
		if ctx.Err() != nil || c.superseded() {
			return
		}
		if th == nil || strings.HasPrefix(th.ThreadID, "team:") {
			continue
		}
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(th.ThreadID))
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		c.refreshPinnedMessages(ctx, th.ThreadID)
	}
}

// pinnedMessageTracker holds the last pinned list announced for each thread,
// and the portals already made pinnable by members.
type pinnedMessageTracker struct {
	mu       sync.Mutex
	sigs     map[string]string // threadID -> comma-joined message IDs
	pinnable map[id.RoomID]bool
}

// markPinnable records roomID as checked; false if it already was.
func (p *pinnedMessageTracker) markPinnable(roomID id.RoomID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pinnable[roomID] {
		return false
	}
	if p.pinnable == nil {
		p.pinnable = make(map[id.RoomID]bool)
	}
	p.pinnable[roomID] = true
	return true
}

func (p *pinnedMessageTracker) get(threadID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sig, ok := p.sigs[threadID]
	return sig, ok
}

func (p *pinnedMessageTracker) set(threadID, sig string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sigs == nil {
		p.sigs = make(map[string]string)
	}
	p.sigs[threadID] = sig
}

// syncPinnedMessages sets threadID's room's pinned events from Teams'
// pinnedItems when they changed.  A chat seen for the first time with no
// pins is only recorded: an empty list would clear pins set on Matrix.
func (c *TeamsClient) syncPinnedMessages(ctx context.Context, threadID string, pinnedItems string) {
	ids := model.ParsePinnedItems(pinnedItems)
	sig := strings.Join(ids, ",")
	c.ensurePinnable(ctx, threadID)
	prev, known := c.pinnedMessages.get(threadID)
	if known && prev == sig {
		return
	}
	if !known && sig == "" {
		c.pinnedMessages.set(threadID, sig)
		return
	}
	if c.Main == nil || c.Main.Bridge == nil {
		return
	}
	portalKey := c.portalKey(threadID)
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil || portal == nil || portal.MXID == "" {
		return // retried on the next pass
	}
	pinned := make([]id.EventID, 0, len(ids))
	for _, messageID := range ids {
		part, err := c.Main.Bridge.DB.Message.GetFirstPartByID(ctx, portalKey.Receiver, networkid.MessageID(messageID))
		if err == nil && part != nil && part.MXID != "" {
			pinned = append(pinned, part.MXID)
		}
	}
	content := &event.Content{Parsed: &event.PinnedEventsEventContent{Pinned: pinned}}
	if _, err := c.Main.Bridge.Bot.SendState(ctx, portal.MXID, event.StatePinnedEvents, "", content, time.Time{}); err != nil {
		log := c.log()
		log.Warn().Err(err).Str("thread_id", threadID).Msg("Failed to set pinned messages")
		return
	}
	c.pinnedMessages.set(threadID, sig)
}

// ensurePinnable lets the members of threadID's room pin messages: it sets
// m.room.pinned_events to power level 0, once per room per run.
func (c *TeamsClient) ensurePinnable(ctx context.Context, threadID string) {
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.Matrix == nil {
		return
	}
	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(threadID))
	if err != nil || portal == nil || portal.MXID == "" || !c.pinnedMessages.markPinnable(portal.MXID) {
		return
	}
	pl, err := c.Main.Bridge.Matrix.GetPowerLevels(ctx, portal.MXID)
	if err != nil || pl == nil || pl.GetEventLevel(event.StatePinnedEvents) <= 0 {
		return
	}
	pl.EnsureEventLevel(event.StatePinnedEvents, 0)
	if _, err := c.Main.Bridge.Bot.SendState(ctx, portal.MXID, event.StatePowerLevels, "", &event.Content{Parsed: pl}, time.Time{}); err != nil {
		log := c.log()
		log.Warn().Err(err).Str("thread_id", threadID).Msg("Failed to let members pin messages")
	}
}

// registerPinHandler adds handleMatrixPinnedEvents to the appservice event
// processor.  Called from Init: the Matrix connector is set up before the
// network connector, and nothing is processed until the bridge starts.
func (t *TeamsConnector) registerPinHandler() {
	if t.Bridge == nil {
		return
	}
	if mc, ok := t.Bridge.Matrix.(*matrix.Connector); ok && mc.EventProcessor != nil {
		mc.EventProcessor.On(event.StatePinnedEvents, t.handleMatrixPinnedEvents)
	}
}

// handleMatrixPinnedEvents writes a user's change of a room's pinned events
// to the chat's pinned messages in Teams.
func (t *TeamsConnector) handleMatrixPinnedEvents(ctx context.Context, evt *event.Event) {
	br := t.Bridge
	if br == nil || evt == nil || evt.Sender == br.Bot.GetMXID() || br.IsGhostMXID(evt.Sender) {
		return // the bot's own (from Teams), or a ghost's
	}
	portal, err := br.GetPortalByMXID(ctx, evt.RoomID)
	if err != nil || portal == nil {
		return
	}
	user, err := br.GetExistingUserByMXID(ctx, evt.Sender)
	if err != nil || user == nil {
		return
	}
	login := user.GetDefaultLogin()
	if login == nil {
		return
	}
	client, ok := login.Client.(*TeamsClient)
	if !ok || client == nil {
		return
	}
	log := client.log()
	_ = evt.Content.ParseRaw(evt.Type)
	content := evt.Content.AsPinnedEvents()
	threadID := string(portal.ID)
	var pinned []string
	for _, mxid := range content.Pinned {
		part, err := br.DB.Message.GetPartByMXID(ctx, mxid)
		if err == nil && part != nil && !slices.Contains(pinned, string(part.ID)) {
			pinned = append(pinned, string(part.ID))
		}
	}
	prev, _ := client.pinnedMessages.get(threadID)
	ids := mergePinnedItems(splitPinnedSig(prev), pinned)
	api, ok := client.getAPI().(pinnedItemsAPI)
	if !ok {
		return
	}
	if err := api.SetPinnedItems(ctx, threadID, ids); err != nil {
		log.Warn().Err(err).Str("thread_id", threadID).Msg("Failed to set pinned messages in Teams")
		return
	}
	// Discovery then finds Teams' list as it is, and leaves the room alone.
	client.pinnedMessages.set(threadID, strings.Join(ids, ","))
	log.Info().Str("thread_id", threadID).Int("pinned", len(ids)).Msg("Pinned messages set in Teams")
}

func splitPinnedSig(sig string) []string {
	if sig == "" {
		return nil
	}
	return strings.Split(sig, ",")
}

// mergePinnedItems is Teams' pinned list after pinning exactly wanted:
// messages newly pinned first (the last in wanted first, as Matrix clients
// add pins at the end), then those already pinned, in Teams' order.
func mergePinnedItems(current, wanted []string) []string {
	var added, kept []string
	for i := len(wanted) - 1; i >= 0; i-- {
		if !slices.Contains(current, wanted[i]) {
			added = append(added, wanted[i])
		}
	}
	for _, id := range current {
		if slices.Contains(wanted, id) {
			kept = append(kept, id)
		}
	}
	return append(added, kept...)
}
