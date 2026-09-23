package connector

// !msteams repair-blank-messages: bridge versions before 7451363 relayed
// some Teams messages with nothing in them (emoticons, GIF-picker GIFs,
// attachments the converter got nothing out of).  This walks each room
// for blank messages from ghosts, fetches their Teams originals again,
// converts them with the current converter, sends the result as the same
// ghost at the original time, redacts the blank event and points the
// message row at the new one, so reactions and replies keep working.  A
// message Teams has deleted is redacted instead, and one that still
// converts to nothing (or only to the unsupported-message placeholder, which
// counts as blank on the way in) is left as it is.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
)

var commandRepairBlankMessages = &commands.FullHandler{
	Func: fnRepairBlankMessages,
	Name: "repair-blank-messages",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Re-send messages an older bridge relayed blank (GIFs, emoticons, attachments) from their Teams originals (--dry-run to only count them)",
		Args:        "[--dry-run]",
	},
	RequiresAdmin: true,
	RequiresLogin: true,
}

func fnRepairBlankMessages(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	client, _ := login.Client.(*TeamsClient)
	if client == nil {
		ce.Reply("Not logged in to Teams.")
		return
	}
	dryRun := len(ce.Args) > 0 && ce.Args[0] == "--dry-run"
	ce.Reply(client.repairBlankMessages(ce.Ctx, dryRun))
}

// blankMessageContent reports whether a message event shows nothing, or
// only the converter's "[Unsupported Teams message: …]" placeholder.
func blankMessageContent(evt *event.Event) bool {
	if evt.Type != event.EventMessage || evt.Unsigned.RedactedBecause != nil {
		return false
	}
	if err := evt.Content.ParseRaw(evt.Type); err != nil {
		return false
	}
	msg := evt.Content.AsMessage()
	if msg == nil {
		return false
	}
	if isUnsupportedPlaceholder(msg.Body) {
		return true
	}
	return strings.TrimSpace(msg.Body) == "" && strings.TrimSpace(msg.FormattedBody) == "" &&
		msg.URL == "" && msg.File == nil
}

func isUnsupportedPlaceholder(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), "[Unsupported Teams message:")
}

func (c *TeamsClient) repairBlankMessages(ctx context.Context, dryRun bool) string {
	log := c.log()
	bot, _ := c.Main.Bridge.Bot.(*matrix.ASIntent)
	if bot == nil {
		return "The bridge bot is not a Matrix appservice intent; cannot read rooms."
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return fmt.Sprintf("Could not list threads: %v", err)
	}
	var blanks, resent, stillBlank, deleted, own, unknown, failed, rooms int
	filter := &mautrix.FilterPart{Types: []event.Type{event.EventMessage}}
	for _, state := range threads {
		if isNonPollableSystemStream(state.ThreadID) {
			continue
		}
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(state.ThreadID))
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		roomCounted := false
		from := ""
		for {
			resp, err := bot.Matrix.Messages(ctx, portal.MXID, from, "", mautrix.DirectionBackward, filter, 500)
			if err != nil {
				log.Warn().Err(err).Str("thread_id", state.ThreadID).Msg("Failed to read room history for blank repair")
				break
			}
			for _, evt := range resp.Chunk {
				if !blankMessageContent(evt) {
					continue
				}
				if !roomCounted {
					rooms++
					roomCounted = true
				}
				ghostID, isGhost := c.Main.Bridge.Matrix.ParseGhostMXID(evt.Sender)
				if !isGhost {
					own++
					continue
				}
				row, err := c.Main.Bridge.DB.Message.GetPartByMXID(ctx, evt.ID)
				if err != nil || row == nil {
					unknown++
					continue
				}
				blanks++
				if dryRun {
					continue
				}
				original, err := c.getAPI().GetMessage(ctx, state.Conversation, string(row.ID))
				if err != nil || original == nil {
					log.Warn().Err(err).Str("thread_id", state.ThreadID).Str("message_id", string(row.ID)).Msg("Failed to fetch the original of a blank message")
					failed++
					continue
				}
				ghost, err := c.Main.Bridge.GetGhostByID(ctx, ghostID)
				if err != nil || ghost == nil {
					failed++
					continue
				}
				converted, err := c.convertTeamsMessage(ctx, portal, ghost.Intent, *original)
				if errors.Is(err, errDeletedMessage) {
					// Deleted on Teams: gone here too.
					if ghostIntent, isAS := ghost.Intent.(*matrix.ASIntent); isAS {
						if _, err := ghostIntent.Matrix.RedactEvent(ctx, portal.MXID, evt.ID); err != nil {
							log.Warn().Err(err).Stringer("event_id", evt.ID).Msg("Failed to redact a deleted message")
						}
					}
					deleted++
					continue
				}
				if err != nil || converted == nil || len(converted.Parts) == 0 {
					stillBlank++
					continue
				}
				var sent []*event.Content
				for _, part := range converted.Parts {
					if part.Content == nil || isUnsupportedPlaceholder(part.Content.Body) ||
						(strings.TrimSpace(part.Content.Body) == "" && part.Content.URL == "" && part.Content.File == nil) {
						continue
					}
					sent = append(sent, &event.Content{Parsed: part.Content, Raw: part.Extra})
				}
				if len(sent) == 0 {
					// Teams shows nothing for it either.
					stillBlank++
					continue
				}
				ts := time.UnixMilli(evt.Timestamp)
				var firstID = evt.ID
				ok := true
				for i, content := range sent {
					sendResp, err := ghost.Intent.SendMessage(ctx, portal.MXID, event.EventMessage, content, &bridgev2.MatrixSendExtra{Timestamp: ts})
					if err != nil {
						log.Warn().Err(err).Str("thread_id", state.ThreadID).Str("message_id", string(row.ID)).Msg("Failed to re-send a blank message")
						ok = false
						break
					}
					if i == 0 {
						firstID = sendResp.EventID
					}
				}
				if !ok {
					failed++
					continue
				}
				if ghostIntent, isAS := ghost.Intent.(*matrix.ASIntent); isAS {
					if _, err := ghostIntent.Matrix.RedactEvent(ctx, portal.MXID, evt.ID); err != nil {
						log.Warn().Err(err).Stringer("event_id", evt.ID).Msg("Failed to redact a blank message")
					}
				}
				row.MXID = firstID
				if err := c.Main.Bridge.DB.Message.Update(ctx, row); err != nil {
					log.Warn().Err(err).Stringer("event_id", firstID).Msg("Failed to point the message row at the re-sent event")
				}
				resent++
			}
			from = resp.End
			if from == "" || len(resp.Chunk) == 0 {
				break
			}
		}
	}
	if dryRun {
		return fmt.Sprintf("%d blank messages from Teams users in %d rooms would be re-sent; %d blank messages of your own and %d without a bridge record would be left.",
			blanks, rooms, own, unknown)
	}
	summary := fmt.Sprintf("Re-sent %d of %d blank messages in %d rooms; %d were deleted on Teams and are now removed here; %d are blank on Teams too and were left.",
		resent, blanks, rooms, deleted, stillBlank)
	if failed > 0 {
		summary += fmt.Sprintf(" %d failed (see the log).", failed)
	}
	if own > 0 || unknown > 0 {
		summary += fmt.Sprintf(" Left alone: %d of your own, %d without a bridge record.", own, unknown)
	}
	return summary
}
