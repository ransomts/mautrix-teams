package connector

// The user's own state of each chat: pinned, muted, marked unread.
//
// Pinned chats: Teams "Pin" <-> the Matrix m.favourite room tag.
//
// Teams keeps the pinned list in a user property (see
// internal/teams/client/chat_state.go), keyed by conversation ID.
// Discovery reads it once per pass and puts the pin state into the chat
// info it announces; bridgev2 then tags the room with the user's double
// puppet.  It only announces a change of pin state (and a pin seen for the
// first time), never "not pinned" on its own: bridgev2 applies an empty tag
// by removing both m.favourite and m.lowpriority, which would undo tags the
// user set on Matrix for chats that were never pinned on Teams.
//
// bridgev2 only applies tags after portal creation when the bridge config
// has tag_only_on_create: false (the default is true).  Matrix -> Teams goes
// through HandleRoomTag, which bridgev2 calls for m.tag room account data
// (which Synapse does not send to appservices, so it does not fire here).
//
// Muted and marked unread are conversation properties (see
// model.ConversationProperties), read from the same conversation list
// discovery walks.  A mute goes to Matrix as the room's push rule (through
// UserLocal.MutedUntil; mute_only_on_create: false is needed after
// creation), a mark as m.marked_unread (a MarkUnread event).  Like pins,
// only changes are announced, and "not muted" or "not marked" seen for the
// first time is not: that would undo what the user set on Matrix.  Teams
// -> Matrix only: Synapse tells the bridge of neither on the Matrix side.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// chatStateAPI is the part of the Teams client that reads and writes
// pinned chats.  It is separate from TeamsAPI so test fakes need not
// implement it; a client without it bridges no pins.
type chatStateAPI interface {
	GetPinnedChats(ctx context.Context) (map[string]int, error)
	SetChatPinned(ctx context.Context, conversationID string, pinned bool) error
}

var (
	_ chatStateAPI                   = (*consumerclient.Client)(nil)
	_ bridgev2.TagHandlingNetworkAPI = (*TeamsClient)(nil)
)

// stateTracker holds the last known on/off state (pinned, muted, marked
// unread) of each thread, so discovery can tell a change from a repeat and
// HandleRoomTag can drop the echo of a tag bridgev2 set from Teams.
type stateTracker struct {
	mu     sync.Mutex
	pinned map[string]bool // threadID -> pinned on Teams
}

// get returns the last known pin state of threadID and whether one is known.
func (p *stateTracker) get(threadID string) (pinned bool, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pinned, known = p.pinned[threadID]
	return
}

func (p *stateTracker) set(threadID string, pinned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pinned == nil {
		p.pinned = make(map[string]bool)
	}
	p.pinned[threadID] = pinned
}

// changed records the state Teams now reports for threadID and reports
// whether to announce it: when it turned on (or is on and seen for the
// first time), or turned off.
func (p *stateTracker) changed(threadID string, on bool) bool {
	prev, known := p.get(threadID)
	p.set(threadID, on)
	return (on && (!known || !prev)) || (!on && known && prev)
}

// observe records the pin state Teams now reports for threadID and returns
// the tag to announce: m.favourite for a new pin (or one seen for the first
// time), "" for a removed pin, nil when nothing changed.
func (p *stateTracker) observe(threadID string, pinned bool) *event.RoomTag {
	if !p.changed(threadID, pinned) {
		return nil
	}
	tag := event.RoomTag("")
	if pinned {
		tag = event.RoomTagFavourite
	}
	return &tag
}

// chatMuteState works out a discovered thread's mute for its chat info,
// like chatPinState: the mute to announce (nil when unchanged) and the mute
// part of the chat-info signature.
func (c *TeamsClient) chatMuteState(thread model.Thread, props model.ConversationProperties) (*time.Time, string) {
	muted := props.Muted()
	sig := ""
	if muted {
		sig = "\x00muted"
	}
	if !c.mutes.changed(thread.ID, muted) {
		return nil, sig
	}
	until := bridgev2.Unmuted
	if muted {
		until = event.MutedForever
	}
	return &until, sig
}

// queueMarkedUnread sends a change of the chat's "marked unread" state to
// Matrix.  It runs on every discovery pass, apart from the chat-info
// signature, since it is an event of its own.
func (c *TeamsClient) queueMarkedUnread(thread model.Thread, props model.ConversationProperties) {
	unread := props.MarkedUnread()
	if c.Login == nil || !c.unreads.changed(thread.ID, unread) {
		return
	}
	c.queueRemoteEvent(&simplevent.MarkUnread{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMarkUnread,
			PortalKey: c.portalKey(thread.ID),
			Sender: bridgev2.EventSender{
				IsFromMe:    true,
				SenderLogin: c.Login.ID,
				Sender:      teamsUserIDToNetworkUserID(c.selfTeamsUserID()),
			},
			Timestamp: time.Now(),
		},
		Unread: unread,
	})
}

// fetchPinnedChats returns the conversation IDs pinned on Teams, or nil and
// false when they could not be read (the pin state of every thread is then
// left as it was).
func (c *TeamsClient) fetchPinnedChats(ctx context.Context) (map[string]int, bool) {
	api, ok := c.getAPI().(chatStateAPI)
	if !ok {
		return nil, false
	}
	pins, err := api.GetPinnedChats(ctx)
	if err != nil {
		log := c.log()
		log.Debug().Err(err).Msg("Failed to read pinned chats")
		return nil, false
	}
	return pins, true
}

// chatPinState works out a discovered thread's pin state for its chat info.
// It returns the user-local info to announce (nil when the pin state did
// not change) and the pin part of the chat-info signature, so a change of
// pin alone re-announces the chat.
func (c *TeamsClient) chatPinState(thread model.Thread, pins map[string]int, pinsKnown bool) (*bridgev2.UserLocalPortalInfo, string) {
	if !pinsKnown {
		pinned, _ := c.pins.get(thread.ID)
		return nil, pinSignature(pinned)
	}
	_, pinned := pins[thread.ConversationID]
	if !pinned {
		_, pinned = pins[thread.ID]
	}
	sig := pinSignature(pinned)
	if tag := c.pins.observe(thread.ID, pinned); tag != nil {
		return &bridgev2.UserLocalPortalInfo{Tag: tag}, sig
	}
	return nil, sig
}

func pinSignature(pinned bool) string {
	if pinned {
		return "\x00pinned"
	}
	return ""
}

// HandleRoomTag pins or unpins the Teams chat when m.favourite is added to
// or removed from the room on Matrix.  Other tags are not bridged.
func (c *TeamsClient) HandleRoomTag(ctx context.Context, msg *bridgev2.MatrixRoomTag) error {
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if msg == nil || msg.Content == nil || msg.Portal == nil {
		return errors.New("missing room tag content")
	}
	pinned := hasFavouriteTag(msg.Content)
	if msg.PrevContent != nil && hasFavouriteTag(msg.PrevContent) == pinned {
		// Some other tag changed.
		return nil
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}
	// Already the Teams state: the echo of a tag set from Teams, or a
	// repeat.
	if known, ok := c.pins.get(threadID); ok && known == pinned {
		return nil
	}
	conversationID, ok := c.pinnableConversationID(ctx, threadID)
	if !ok {
		return nil
	}
	api, ok := c.getAPI().(chatStateAPI)
	if !ok {
		return errors.New("pinning chats is not supported by this client")
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if err := api.SetChatPinned(ctx, conversationID, pinned); err != nil {
		return err
	}
	c.pins.set(threadID, pinned)
	log := c.log()
	log.Debug().Str("thread_id", threadID).Bool("pinned", pinned).Msg("Set Teams chat pin from Matrix tag")
	return nil
}

// pinnableConversationID returns the conversation ID the pinned list uses
// for threadID ("48:notes" for the notes chat, otherwise usually the thread
// ID), and false for portals that are not chats: team spaces have no
// thread state, and channels are not pinned this way.
func (c *TeamsClient) pinnableConversationID(ctx context.Context, threadID string) (string, bool) {
	if isChannelThread(threadID) || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return "", false
	}
	row, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID)
	if err != nil || row == nil {
		return "", false
	}
	if conv := strings.TrimSpace(row.Conversation); conv != "" {
		return conv, true
	}
	return threadID, true
}

func hasFavouriteTag(content *event.TagEventContent) bool {
	if content == nil {
		return false
	}
	_, ok := content.Tags[event.RoomTagFavourite]
	return ok
}
