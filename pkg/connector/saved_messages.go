package connector

// Saved messages, both ways.  Teams -> Matrix: discovery notes each
// conversation's lastRcMetadataVersion (see
// internal/teams/client/saved_messages.go); the saved messages of those
// whose version changed are read again, and the whole list, mapped to
// bridged rooms and events with the sender and a preview, is published as
// the account data event fi.mau.teams.saved through the double puppet.
// Matrix -> Teams: the bot commands save and unsave.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

const (
	savedAccountDataType = "fi.mau.teams.saved"
	savedPreviewLength   = 200
)

// savedAPI is the part of the Teams client that reads and writes saved
// messages; separate from TeamsAPI so test fakes need not implement it.
type savedAPI interface {
	GetSavedMessages(ctx context.Context, conversationID string) ([]consumerclient.SavedMessage, error)
	SetMessageSaved(ctx context.Context, conversationID string, rootID string, messageID string, saved bool) error
}

var _ savedAPI = (*consumerclient.Client)(nil)

// savedItem is one published saved message.
type savedItem struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
	SavedTS   int64  `json:"saved_ts"`
	RoomID    string `json:"room_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Who       string `json:"who,omitempty"`
	Preview   string `json:"preview,omitempty"`
	TS        int64  `json:"ts,omitempty"`
}

// savedChat is a conversation whose saved messages are to be read.
type savedChat struct {
	threadID, conversationID, version string
}

// savedTracker holds what is known of the saved messages of each chat.
type savedTracker struct {
	mu        sync.Mutex
	versions  map[string]string                        // threadID -> lastRcMetadataVersion read
	saved     map[string][]consumerclient.SavedMessage // threadID -> its saved messages
	previews  map[string]savedItem                     // event ID -> sender, who, preview, ts
	pending   map[string]savedChat                     // threadID -> chats found changed, not yet read
	published string                                   // signature of the list last published
	// incomplete is set while a published message has no bridged event
	// (yet), so the list is mapped again on each pass.
	incomplete bool
}

// noteVersion records a conversation's rc metadata version from discovery,
// queueing its saved messages for reading when it changed.
func (s *savedTracker) noteVersion(threadID, conversationID, version string) {
	if version == "" || version == "0" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions[threadID] == version {
		return
	}
	if s.pending == nil {
		s.pending = make(map[string]savedChat)
	}
	s.pending[threadID] = savedChat{threadID, conversationID, version}
}

func (s *savedTracker) takePending() []savedChat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]savedChat, 0, len(s.pending))
	for _, chat := range s.pending {
		out = append(out, chat)
	}
	s.pending = nil
	return out
}

func (s *savedTracker) store(chat savedChat, saved []consumerclient.SavedMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.versions == nil {
		s.versions = make(map[string]string)
		s.saved = make(map[string][]consumerclient.SavedMessage)
	}
	s.versions[chat.threadID] = chat.version
	if len(saved) == 0 {
		delete(s.saved, chat.threadID)
	} else {
		s.saved[chat.threadID] = saved
	}
}

// syncSavedMessages reads the saved messages of the chats discovery found
// changed and publishes the list when it changed.
func (c *TeamsClient) syncSavedMessages(ctx context.Context) {
	chats := c.saved.takePending()
	c.saved.mu.Lock()
	settled := c.saved.published != "" && !c.saved.incomplete
	c.saved.mu.Unlock()
	if len(chats) == 0 && settled {
		return
	}
	api, ok := c.getAPI().(savedAPI)
	if !ok {
		return
	}
	log := c.log()
	for _, chat := range chats {
		saved, err := api.GetSavedMessages(ctx, chat.conversationID)
		if err != nil {
			// Not stored: discovery finds it changed again next pass.
			log.Debug().Err(err).Str("thread_id", chat.threadID).Msg("Failed to read saved messages")
			continue
		}
		c.saved.store(chat, saved)
	}
	c.publishSavedMessages(ctx)
}

// publishSavedMessages publishes the saved messages of every chat, newest
// saved first, unless the list is what was published last.
func (c *TeamsClient) publishSavedMessages(ctx context.Context) {
	if c.Main == nil || c.Main.Bridge == nil || c.Login == nil {
		return
	}
	c.saved.mu.Lock()
	var items []savedItem
	for threadID, saved := range c.saved.saved {
		for _, m := range saved {
			items = append(items, savedItem{ThreadID: threadID, MessageID: m.MessageID, SavedTS: m.SavedAt})
		}
	}
	c.saved.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SavedTS != items[j].SavedTS {
			return items[i].SavedTS > items[j].SavedTS
		}
		return items[i].MessageID > items[j].MessageID
	})
	receiver := c.portalKey("").Receiver
	var sig strings.Builder
	sig.WriteString("v1")
	incomplete := false
	for i := range items {
		item := &items[i]
		if portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(item.ThreadID)); err == nil && portal != nil && portal.MXID != "" {
			item.RoomID = string(portal.MXID)
			part, err := c.Main.Bridge.DB.Message.GetFirstPartByID(ctx, receiver, networkid.MessageID(item.MessageID))
			if err == nil && part != nil {
				item.EventID = string(part.MXID)
				c.fillSavedPreview(ctx, item)
			}
		}
		incomplete = incomplete || item.EventID == ""
		sig.WriteString("\x00" + item.ThreadID + "/" + item.MessageID + "/" + item.EventID)
	}
	c.saved.mu.Lock()
	c.saved.incomplete = incomplete
	unchanged := sig.String() == c.saved.published
	c.saved.mu.Unlock()
	if unchanged {
		return
	}
	if items == nil {
		items = []savedItem{}
	}
	if err := c.publishAccountData(ctx, savedAccountDataType, map[string]any{
		"items":      items,
		"updated_ts": time.Now().UnixMilli(),
	}); err != nil {
		log := c.log()
		log.Debug().Err(err).Msg("Failed to publish saved messages")
		return
	}
	c.saved.mu.Lock()
	c.saved.published = sig.String()
	c.saved.mu.Unlock()
}

// fillSavedPreview adds the sender, their name, the start of the text and
// the time of a saved message's Matrix event, fetched once per event.
func (c *TeamsClient) fillSavedPreview(ctx context.Context, item *savedItem) {
	c.saved.mu.Lock()
	cached, ok := c.saved.previews[item.EventID]
	c.saved.mu.Unlock()
	if !ok {
		cached, ok = c.fetchSavedPreview(ctx, id.RoomID(item.RoomID), id.EventID(item.EventID))
		if !ok {
			return
		}
		c.saved.mu.Lock()
		if c.saved.previews == nil {
			c.saved.previews = make(map[string]savedItem)
		}
		c.saved.previews[item.EventID] = cached
		c.saved.mu.Unlock()
	}
	item.Sender, item.Who, item.Preview, item.TS = cached.Sender, cached.Who, cached.Preview, cached.TS
}

func (c *TeamsClient) fetchSavedPreview(ctx context.Context, roomID id.RoomID, eventID id.EventID) (savedItem, bool) {
	if c.Login == nil || c.Login.User == nil {
		return savedItem{}, false
	}
	asIntent, ok := c.Login.User.DoublePuppet(ctx).(*matrix.ASIntent)
	if !ok || asIntent == nil {
		return savedItem{}, false
	}
	evt, err := asIntent.Matrix.GetEvent(ctx, roomID, eventID)
	if err != nil || evt == nil {
		return savedItem{}, false
	}
	out := savedItem{Sender: string(evt.Sender), TS: evt.Timestamp}
	if body, _ := evt.Content.Raw["body"].(string); body != "" {
		out.Preview = truncateRunes(strings.Join(strings.Fields(body), " "), savedPreviewLength)
	}
	if evt.Sender == c.Login.UserMXID {
		out.Who = "You"
	} else if ghost, err := c.Main.Bridge.GetGhostByMXID(ctx, evt.Sender); err == nil && ghost != nil && ghost.Name != "" {
		out.Who = ghost.Name
	}
	return out, true
}

// truncateRunes cuts s to at most n runes, marking a cut with "…".
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var cmdSave = &commands.FullHandler{
	Func: fnSave,
	Name: "save",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Save a bridged message in Teams (\"Save this message\").",
		Args:        "<event ID>",
	},
	RequiresLogin: true,
}

var cmdUnsave = &commands.FullHandler{
	Func: fnSave,
	Name: "unsave",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Remove a bridged message from your saved messages in Teams.",
		Args:        "<event ID>",
	},
	RequiresLogin: true,
}

func fnSave(ce *commands.Event) {
	save := ce.Command == "save"
	if len(ce.Args) != 1 {
		ce.Reply("Usage: `$cmdprefix %s <event ID>`", ce.Command)
		return
	}
	part, err := ce.Bridge.DB.Message.GetPartByMXID(ce.Ctx, id.EventID(ce.Args[0]))
	if err != nil || part == nil {
		ce.Reply("%s is not a bridged Teams message.", ce.Args[0])
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	api, ok := client.getAPI().(savedAPI)
	if !ok {
		ce.Reply("This Teams client cannot save messages.")
		return
	}
	threadID := string(part.Room.ID)
	conversation := client.conversationID(ce.Ctx, threadID)
	if err := api.SetMessageSaved(ce.Ctx, conversation, string(part.ThreadRoot), string(part.ID), save); err != nil {
		ce.Reply("Failed: %s", err)
		return
	}
	// Read the chat's saved messages again now, rather than when discovery
	// next sees its version change.
	if saved, err := api.GetSavedMessages(ce.Ctx, conversation); err == nil {
		// No version: discovery reads it once more, which is harmless.
		client.saved.store(savedChat{threadID: threadID, conversationID: conversation}, saved)
		client.publishSavedMessages(ce.Ctx)
	}
	if save {
		ce.Reply("Saved in Teams.")
	} else {
		ce.Reply("Removed from your saved messages in Teams.")
	}
}
