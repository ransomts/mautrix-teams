package connector

// !msteams repair-system-messages: bridge versions before 0a196ef minted a
// ghost for each conversation's thread ID and relayed Teams system messages
// as that ghost's text, so a channel shows a member named
// "19:…@thread.tacv2" posting runs of "8:orgid:…".  This finds those
// messages through the bridge database, fetches the originals from Teams
// again, posts each as a readable notice from the "Teams" ghost at its
// original time, redacts the old text as the ghost that sent it, and makes
// that ghost leave.  Nothing reaches Teams: every event is one of the
// bridge's own users.  Running it again is safe: a message whose notice is
// already in the room (same sender, same original timestamp) is skipped.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

var commandRepairSystemMessages = &commands.FullHandler{
	Func: fnRepairSystemMessages,
	Name: "repair-system-messages",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Replace Teams system messages an older bridge relayed as raw text with readable notices (--dry-run to only count them)",
		Args:        "[--dry-run]",
	},
	RequiresAdmin: true,
	RequiresLogin: true,
}

func fnRepairSystemMessages(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	client, _ := login.Client.(*TeamsClient)
	if client == nil {
		ce.Reply("Not logged in to Teams.")
		return
	}
	dryRun := len(ce.Args) > 0 && ce.Args[0] == "--dry-run"
	ce.Reply(client.repairSystemMessages(ce.Ctx, dryRun))
}

// relayedSystemMessage is a message row sent by a thread-ID ghost.
type relayedSystemMessage struct {
	threadID string
	id       string
	mxid     id.EventID
}

// listRelayedSystemMessages finds the message rows whose sender is a
// conversation rather than a person, grouped by thread.
func (c *TeamsClient) listRelayedSystemMessages(ctx context.Context) (map[string][]relayedSystemMessage, error) {
	rows, err := c.Main.DB.Query(ctx, `
		SELECT room_id, id, mxid FROM message
		WHERE bridge_id=$1 AND sender_id LIKE '19:%'
		ORDER BY room_id, timestamp
	`, c.Main.Bridge.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byThread := make(map[string][]relayedSystemMessage)
	for rows.Next() {
		var m relayedSystemMessage
		if err := rows.Scan(&m.threadID, &m.id, &m.mxid); err != nil {
			return nil, err
		}
		byThread[m.threadID] = append(byThread[m.threadID], m)
	}
	return byThread, rows.Err()
}

// existingNoticeTimestamps lists, by origin timestamp (ms), the events the
// system ghost has already sent in roomID, so a repair run twice does not
// post a notice twice.
func (c *TeamsClient) existingNoticeTimestamps(ctx context.Context, roomID id.RoomID, ghostMXID id.UserID) map[int64]id.EventID {
	out := make(map[int64]id.EventID)
	bot, _ := c.Main.Bridge.Bot.(*matrix.ASIntent)
	if bot == nil {
		return out
	}
	filter := &mautrix.FilterPart{Senders: []id.UserID{ghostMXID}, Types: []event.Type{event.EventMessage}}
	from := ""
	for {
		resp, err := bot.Matrix.Messages(ctx, roomID, from, "", mautrix.DirectionBackward, filter, 500)
		if err != nil {
			return out
		}
		for _, evt := range resp.Chunk {
			if evt.Unsigned.RedactedBecause == nil {
				out[evt.Timestamp] = evt.ID
			}
		}
		from = resp.End
		if from == "" || len(resp.Chunk) == 0 {
			return out
		}
	}
}

func (c *TeamsClient) repairSystemMessages(ctx context.Context, dryRun bool) string {
	log := c.log()
	byThread, err := c.listRelayedSystemMessages(ctx)
	if err != nil {
		return fmt.Sprintf("Could not list relayed system messages: %v", err)
	}
	if len(byThread) == 0 {
		return "No relayed system messages found."
	}
	threads := make([]string, 0, len(byThread))
	total := 0
	for threadID, msgs := range byThread {
		threads = append(threads, threadID)
		total += len(msgs)
	}
	sort.Strings(threads)

	systemGhost, err := c.Main.Bridge.GetGhostByID(ctx, networkid.UserID(systemSenderID))
	if err != nil || systemGhost == nil {
		return fmt.Sprintf("Could not get the %s ghost: %v", systemSenderName, err)
	}
	if !dryRun {
		name := systemSenderName
		systemGhost.UpdateInfo(ctx, &bridgev2.UserInfo{Name: &name})
	}

	var replaced, redacted, already, failed, left int
	var problems []string
	for _, threadID := range threads {
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(threadID))
		if err != nil || portal == nil || portal.MXID == "" {
			problems = append(problems, fmt.Sprintf("%s: no room", threadID))
			continue
		}
		state, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID)
		if err != nil || state == nil {
			problems = append(problems, fmt.Sprintf("%s: no thread state", portal.Name))
			continue
		}
		originals, err := c.getAPI().ListMessagesPaginated(ctx, state.Conversation, 1000, "")
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: fetching messages failed: %v", portal.Name, err))
			continue
		}
		byID := make(map[string]model.RemoteMessage, len(originals))
		for _, msg := range originals {
			byID[strings.TrimSpace(msg.MessageID)] = msg
		}
		oldGhost, _ := c.Main.Bridge.GetExistingGhostByID(ctx, networkid.UserID(threadID))
		var oldIntent *matrix.ASIntent
		if oldGhost != nil {
			oldIntent, _ = oldGhost.Intent.(*matrix.ASIntent)
		}
		if oldIntent == nil {
			problems = append(problems, fmt.Sprintf("%s: the old ghost is gone; nothing redacted", portal.Name))
		}
		existing := c.existingNoticeTimestamps(ctx, portal.MXID, systemGhost.Intent.GetMXID())

		joined := false
		for _, relayed := range byThread[threadID] {
			original, found := byID[relayed.id]
			activity, ok := parseSystemMessage(original.MessageType, original.RawContent)
			if found && ok {
				if noticeID, done := existing[original.Timestamp.UnixMilli()]; done {
					// An earlier run posted this one; make sure the row
					// points at it and move on.
					already++
					if !dryRun {
						if row, err := c.Main.Bridge.DB.Message.GetPartByMXID(ctx, relayed.mxid); err == nil && row != nil {
							row.MXID = noticeID
							row.SenderID = networkid.UserID(systemSenderID)
							row.SenderMXID = systemGhost.Intent.GetMXID()
							_ = c.Main.Bridge.DB.Message.Update(ctx, row)
						}
					}
					continue
				}
				if dryRun {
					replaced++
					continue
				}
				if !joined {
					if err := systemGhost.Intent.EnsureJoined(ctx, portal.MXID); err != nil {
						problems = append(problems, fmt.Sprintf("%s: %s ghost could not join: %v", portal.Name, systemSenderName, err))
						failed += len(byThread[threadID])
						break
					}
					joined = true
				}
				content := c.systemNoticeContent(ctx, activity, threadID)
				sendResp, err := systemGhost.Intent.SendMessage(ctx, portal.MXID, event.EventMessage,
					&event.Content{Parsed: content}, &bridgev2.MatrixSendExtra{Timestamp: original.Timestamp})
				if err != nil {
					log.Warn().Err(err).Str("thread_id", threadID).Str("message_id", relayed.id).Msg("Failed to send repaired system notice")
					failed++
					continue
				}
				if row, err := c.Main.Bridge.DB.Message.GetPartByMXID(ctx, relayed.mxid); err == nil && row != nil {
					row.MXID = sendResp.EventID
					row.SenderID = networkid.UserID(systemSenderID)
					row.SenderMXID = systemGhost.Intent.GetMXID()
					if err := c.Main.Bridge.DB.Message.Update(ctx, row); err != nil {
						log.Warn().Err(err).Msg("Failed to point the message row at the notice")
					}
				}
				replaced++
			} else {
				// Not found again, or bookkeeping nobody needs to read:
				// the raw text goes away without a replacement.
				redacted++
				if dryRun {
					continue
				}
			}
			if oldIntent != nil {
				if _, err := oldIntent.Matrix.RedactEvent(ctx, portal.MXID, relayed.mxid); err != nil {
					log.Warn().Err(err).Str("thread_id", threadID).Stringer("event_id", relayed.mxid).Msg("Failed to redact relayed system message")
				}
			}
		}
		if oldIntent != nil && !dryRun {
			if _, err := oldIntent.Matrix.LeaveRoom(ctx, portal.MXID); err == nil {
				left++
			}
		}
	}

	if dryRun {
		return fmt.Sprintf("%d relayed system messages in %d rooms: %d would get a notice, %d would be removed without one, %d already have one.",
			total, len(threads), replaced, redacted, already)
	}
	summary := fmt.Sprintf("Replaced %d system messages with notices and removed %d without one in %d rooms; %d already had a notice; %d old ghosts left.",
		replaced, redacted, len(threads), already, left)
	if failed > 0 {
		summary += fmt.Sprintf(" %d could not be replaced (see the log).", failed)
	}
	if len(problems) > 0 {
		summary += "\n" + strings.Join(problems, "\n")
	}
	return summary
}
