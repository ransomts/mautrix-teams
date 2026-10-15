package connector

// The chat bridge bot command: pin, mute or mark a chat unread in Teams
// from Matrix.  Synapse sends neither room tags nor push rules to
// appservices, so these are commands rather than echoes of the Matrix
// state; discovery then brings that state over from Teams as usual (see
// chat_state.go).

import (
	"context"
	"errors"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/id"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

const chatUsage = "<pin|unpin|mute|unmute|unread> [<room ID>]"

// chatActionAPI is the part of the Teams client the chat command uses;
// separate from TeamsAPI so test fakes need not have it.
type chatActionAPI interface {
	SetChatPinned(ctx context.Context, conversationID string, pinned bool) error
	SetChatMuted(ctx context.Context, conversationID string, muted bool) error
	MarkChatUnread(ctx context.Context, conversationID string, horizon string, now time.Time) error
}

var cmdChat = &commands.FullHandler{
	Func: fnChat,
	Name: "chat",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Pin, mute or mark unread a Teams chat: this room's, or the room named.",
		Args:        chatUsage,
	},
	RequiresLogin: true,
}

func fnChat(ce *commands.Event) {
	if len(ce.Args) < 1 || len(ce.Args) > 2 {
		ce.Reply("Usage: `$cmdprefix chat %s`", chatUsage)
		return
	}
	action := strings.ToLower(ce.Args[0])
	portal := ce.Portal
	if len(ce.Args) == 2 {
		var err error
		portal, err = ce.Bridge.GetPortalByMXID(ce.Ctx, id.RoomID(ce.Args[1]))
		if err != nil || portal == nil {
			ce.Reply("%s is not a bridged Teams chat.", ce.Args[1])
			return
		}
	}
	if portal == nil {
		ce.Reply("Run this in a bridged room, or name the room: `$cmdprefix chat %s`", chatUsage)
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	if err := client.chatAction(ce.Ctx, portal, action); err != nil {
		var upe consumerclient.UserPropertiesError
		if errors.As(err, &upe) && upe.BodySnippet != "" {
			ce.Reply("Teams chat %s failed: %s: %s", action, err, upe.BodySnippet)
		} else {
			ce.Reply("Teams chat %s failed: %s", action, err)
		}
		return
	}
	ce.Reply("Teams chat %s: done (Matrix follows within a minute).", action)
}

// chatAction pins, unpins, mutes, unmutes or marks unread portal's chat in Teams.
func (c *TeamsClient) chatAction(ctx context.Context, portal *bridgev2.Portal, action string) error {
	api, ok := c.getAPI().(chatActionAPI)
	if !ok {
		return errorf("this Teams client cannot change chats")
	}
	threadID := string(portal.ID)
	switch action {
	case "pin", "unpin":
		conv, ok := c.pinnableConversationID(ctx, threadID)
		if !ok {
			return errorf("Teams cannot pin this chat (channels are not pinned this way)")
		}
		return api.SetChatPinned(ctx, conv, action == "pin")
	case "mute", "unmute":
		return api.SetChatMuted(ctx, c.conversationID(ctx, threadID), action == "mute")
	case "unread":
		horizon, _ := c.horizons.Load(threadID)
		h, _ := horizon.(string)
		return api.MarkChatUnread(ctx, c.conversationID(ctx, threadID), h, time.Now())
	default:
		return errorf("unknown action %q: %s", action, chatUsage)
	}
}

// conversationID is the Teams conversation ID of threadID's chat: they
// differ for Teams' own streams (Notes is the conversation 48:notes, its
// thread teamsstream_notes_…).
func (c *TeamsClient) conversationID(ctx context.Context, threadID string) string {
	if c.Main != nil && c.Main.DB != nil && c.Login != nil {
		if row, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID); err == nil && row != nil {
			if conv := strings.TrimSpace(row.Conversation); conv != "" {
				return conv
			}
		}
	}
	return threadID
}
