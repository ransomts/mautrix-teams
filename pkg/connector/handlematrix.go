package connector

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	internalbridge "go.mau.fi/mautrix-teams/internal/bridge"
	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

func (c *TeamsClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	log := c.log()
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}
	if msg == nil || msg.Content == nil {
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return nil, errors.New("missing thread id")
	}

	log.Debug().
		Str("thread_id", threadID).
		Str("msg_type", string(msg.Content.MsgType)).
		Bool("has_reply", msg.ReplyTo != nil && msg.ReplyTo.ID != "").
		Msg("Handling outbound Matrix message")

	api := c.getAPI()

	clientMessageID := consumerclient.GenerateClientMessageID()

	now := time.Now().UTC()
	pendingMessage := &database.Message{
		ID:        networkid.MessageID(clientMessageID),
		SenderID:  teamsUserIDToNetworkUserID(c.Meta.TeamsUserID),
		Timestamp: now,
	}
	msg.AddPendingToSave(pendingMessage, networkid.TransactionID(clientMessageID), nil)

	var err error
	switch msg.Content.MsgType {
	case event.MsgText:
		body := msg.Content.Body
		if msg.Content.Format == event.FormatHTML && msg.Content.FormattedBody != "" {
			body = msg.Content.FormattedBody
		}
		// Convert Matrix mention pills to Teams mention spans.
		body, mentionProps := c.convertMatrixMentionsToTeams(body)
		if msg.ReplyTo != nil && msg.ReplyTo.ID != "" {
			replyToID := string(msg.ReplyTo.ID)
			body = c.buildTeamsReplyHTML(ctx, threadID, replyToID, body)
			if len(mentionProps) > 0 {
				_, err = api.SendReplyWithMentions(ctx, threadID, body, c.Meta.TeamsUserID, clientMessageID, replyToID, mentionProps)
			} else {
				_, err = api.SendReplyWithID(ctx, threadID, body, c.Meta.TeamsUserID, clientMessageID, replyToID)
			}
		} else if len(mentionProps) > 0 {
			_, err = api.SendMessageWithMentions(ctx, threadID, body, c.Meta.TeamsUserID, clientMessageID, mentionProps)
		} else {
			_, err = api.SendMessageWithID(ctx, threadID, body, c.Meta.TeamsUserID, clientMessageID)
		}
	case event.MsgImage:
		title, gifURL, ok := extractOutboundGIF(msg.Content)
		if !ok {
			err = c.sendOutboundAttachment(ctx, msg.Portal.MXID, threadID, msg.Content, clientMessageID)
			break
		}
		_, err = api.SendGIFWithID(ctx, threadID, gifURL, title, c.Meta.TeamsUserID, clientMessageID)
	case event.MsgFile, event.MsgVideo, event.MsgAudio:
		err = c.sendOutboundAttachment(ctx, msg.Portal.MXID, threadID, msg.Content, clientMessageID)
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	if err != nil {
		log.Warn().Err(err).Str("thread_id", threadID).Str("msg_type", string(msg.Content.MsgType)).Msg("Failed to send message to Teams")
		msg.RemovePending(networkid.TransactionID(clientMessageID))
		return nil, wrapTeamsSendError(err)
	}
	log.Debug().Str("thread_id", threadID).Str("client_message_id", clientMessageID).Msg("Sent message to Teams")
	c.recordSelfMessage(clientMessageID)

	return &bridgev2.MatrixMessageResponse{
		DB:          pendingMessage,
		Pending:     true,
		StreamOrder: now.UnixMilli(),
	}, nil
}

var errUnsupportedReactionEmoji = bridgev2.WrapErrorInStatus(errors.New("unsupported reaction emoji")).
	WithErrorAsMessage().
	WithIsCertain(true).
	WithSendNotice(true).
	WithErrorReason(event.MessageStatusUnsupported)

func (c *TeamsClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	if !c.IsLoggedIn() {
		return bridgev2.MatrixReactionPreResponse{}, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return bridgev2.MatrixReactionPreResponse{}, err
	}
	if msg == nil || msg.Content == nil {
		return bridgev2.MatrixReactionPreResponse{}, errors.New("missing reaction content")
	}
	if strings.TrimSpace(c.Meta.TeamsUserID) == "" {
		return bridgev2.MatrixReactionPreResponse{}, errors.New("missing teams user id")
	}
	emoji := strings.TrimSpace(msg.Content.RelatesTo.Key)
	emotionKey, ok := MapEmojiToEmotionKey(emoji)
	if !ok {
		return bridgev2.MatrixReactionPreResponse{}, errUnsupportedReactionEmoji
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID: teamsUserIDToNetworkUserID(c.Meta.TeamsUserID),
		EmojiID:  networkid.EmojiID(emotionKey),
		Emoji:    emoji,
	}, nil
}

func (c *TeamsClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}
	if msg == nil || msg.Content == nil {
		return nil, errors.New("missing reaction content")
	}
	if msg.TargetMessage == nil || msg.TargetMessage.ID == "" {
		return nil, bridgev2.ErrTargetMessageNotFound
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return nil, errors.New("missing thread id")
	}

	emotionKey := ""
	emoji := strings.TrimSpace(msg.Content.RelatesTo.Key)
	if msg.PreHandleResp != nil && msg.PreHandleResp.EmojiID != "" {
		emotionKey = string(msg.PreHandleResp.EmojiID)
		if msg.PreHandleResp.Emoji != "" {
			emoji = msg.PreHandleResp.Emoji
		}
	}
	if emotionKey == "" {
		var ok bool
		emotionKey, ok = MapEmojiToEmotionKey(emoji)
		if !ok {
			return nil, errUnsupportedReactionEmoji
		}
	}

	teamsMessageID := NormalizeTeamsReactionTargetMessageID(string(msg.TargetMessage.ID))
	if teamsMessageID == "" {
		return nil, fmt.Errorf("missing teams message id for reaction target %s", msg.TargetMessage.ID)
	}
	_, err := c.getAPI().AddReaction(ctx, threadID, teamsMessageID, emotionKey, time.Now().UTC().UnixMilli())
	if err != nil {
		return nil, err
	}

	return &database.Reaction{
		EmojiID: networkid.EmojiID(emotionKey),
		Emoji:   emoji,
	}, nil
}

func (c *TeamsClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if msg == nil || msg.TargetReaction == nil {
		return nil
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}

	emotionKey := strings.TrimSpace(string(msg.TargetReaction.EmojiID))
	if emotionKey == "" {
		var ok bool
		emotionKey, ok = MapEmojiToEmotionKey(msg.TargetReaction.Emoji)
		if !ok {
			return nil
		}
	}

	teamsMessageID := NormalizeTeamsReactionTargetMessageID(string(msg.TargetReaction.MessageID))
	if teamsMessageID == "" {
		return errors.New("missing teams message id for reaction removal")
	}
	_, err := c.getAPI().RemoveReaction(ctx, threadID, teamsMessageID, emotionKey)
	return err
}

func (c *TeamsClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if msg == nil || !msg.IsTyping {
		return nil
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}
	_, err := c.getAPI().SendTypingIndicator(ctx, threadID, c.Meta.TeamsUserID)
	return err
}

func (c *TeamsClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if msg == nil {
		return nil
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}
	if !c.shouldSendReceipt(threadID) {
		return nil
	}
	horizon := consumerclient.ConsumptionHorizonNow(time.Now().UTC())
	_, err := c.getAPI().SetConsumptionHorizon(ctx, threadID, horizon)
	return err
}

func (c *TeamsClient) shouldSendReceipt(threadID string) bool {
	c.unreadMu.Lock()
	defer c.unreadMu.Unlock()
	if c.unreadSeen == nil {
		c.unreadSeen = make(map[string]bool)
	}
	if c.unreadSent == nil {
		c.unreadSent = make(map[string]bool)
	}
	if !c.unreadSeen[threadID] {
		return false
	}
	if c.unreadSent[threadID] {
		return false
	}
	c.unreadSent[threadID] = true
	return true
}

func (c *TeamsClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	log := c.log()
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if msg == nil || msg.Content == nil || msg.EditTarget == nil {
		return errors.New("missing edit content or target")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}
	teamsMessageID := string(msg.EditTarget.ID)
	if teamsMessageID == "" {
		return errors.New("missing teams message id for edit target")
	}
	log.Debug().Str("thread_id", threadID).Str("target_id", teamsMessageID).Msg("Editing Teams message")
	newBody := msg.Content.Body
	if msg.Content.Format == event.FormatHTML && msg.Content.FormattedBody != "" {
		newBody = msg.Content.FormattedBody
	}
	err := c.getAPI().EditMessage(ctx, threadID, teamsMessageID, newBody, c.Meta.TeamsUserID)
	if err != nil {
		log.Warn().Err(err).Str("thread_id", threadID).Str("target_id", teamsMessageID).Msg("Failed to edit Teams message")
	}
	return err
}

func (c *TeamsClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	log := c.log()
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	if msg == nil || msg.TargetMessage == nil {
		return errors.New("missing redaction target")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return errors.New("missing thread id")
	}
	teamsMessageID := string(msg.TargetMessage.ID)
	if teamsMessageID == "" {
		return errors.New("missing teams message id for delete target")
	}
	log.Debug().Str("thread_id", threadID).Str("target_id", teamsMessageID).Msg("Deleting Teams message")
	return c.getAPI().DeleteMessage(ctx, threadID, teamsMessageID, c.Meta.TeamsUserID)
}

// buildTeamsReplyHTML fetches the original message and wraps body with a populated blockquote.
// If the original message can't be fetched, falls back to an empty blockquote.
func (c *TeamsClient) buildTeamsReplyHTML(ctx context.Context, threadID, replyToMessageID, body string) string {
	// Resolve the conversation ID (e.g. "48:notes") from thread state,
	// since the Teams API uses that rather than the portal thread ID.
	conversationID := threadID
	if row, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID); err == nil && row != nil && row.Conversation != "" {
		conversationID = row.Conversation
	}

	api := c.getAPI()
	if api != nil {
		origMsg, err := api.GetMessage(ctx, conversationID, replyToMessageID)
		if err == nil && origMsg != nil {
			senderName := origMsg.IMDisplayName
			if senderName == "" {
				senderName = origMsg.TokenDisplayName
			}
			snippet := origMsg.Body
			if snippet == "" {
				snippet = origMsg.FormattedBody
			}
			// Use placeholder for media-only messages.
			if snippet == "" && len(origMsg.InlineImages) > 0 {
				snippet = "\U0001f4f7"
			} else if snippet == "" && origMsg.PropertiesFiles != "" {
				snippet = "\U0001f4ce"
			} else if snippet == "" && len(origMsg.GIFs) > 0 {
				snippet = "GIF"
			}
			// Truncate long snippets.
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			return fmt.Sprintf(
				`<blockquote itemtype="http://schema.skype.com/Reply" itemid="%s"><strong>%s</strong><br>%s</blockquote>%s`,
				html.EscapeString(replyToMessageID),
				html.EscapeString(senderName),
				html.EscapeString(snippet),
				body,
			)
		}
	}

	// Fallback: empty blockquote (better than nothing).
	return fmt.Sprintf(
		`<blockquote itemtype="http://schema.skype.com/Reply" itemid="%s"></blockquote>%s`,
		html.EscapeString(replyToMessageID), body,
	)
}

// wrapTeamsReplyHTML wraps body with a reply blockquote (no quoted content).
// Kept for backward compatibility; prefer buildTeamsReplyHTML.
func wrapTeamsReplyHTML(replyToMessageID string, body string) string {
	return fmt.Sprintf(
		`<blockquote itemtype="http://schema.skype.com/Reply" itemid="%s"></blockquote>%s`,
		html.EscapeString(replyToMessageID), body,
	)
}

func (c *TeamsClient) sendOutboundAttachment(ctx context.Context, roomMXID id.RoomID, threadID string, content *event.MessageEventContent, clientMessageID string) error {
	send := func(ctx context.Context, threadID, filename string, data []byte, caption string) error {
		_, err := c.sendAttachmentMessageWithClientMessageID(ctx, threadID, filename, data, caption, clientMessageID)
		return err
	}
	download := func(ctx context.Context, mxcURL string, file *event.EncryptedFileInfo) ([]byte, error) {
		return c.downloadMatrixMedia(ctx, mxcURL, file)
	}
	return internalbridge.HandleOutboundMatrixFile(ctx, roomMXID, threadID, content, download, send, &c.Login.Log)
}

// wrapTeamsSendError wraps consumer client errors in bridgev2.MessageStatus
// to provide structured delivery feedback to the Matrix user.
func wrapTeamsSendError(err error) error {
	var sendErr consumerclient.SendMessageError
	if errors.As(err, &sendErr) {
		return bridgev2.WrapErrorInStatus(err).
			WithErrorAsMessage().
			WithIsCertain(true).
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusGenericError)
	}
	var retryErr consumerclient.RetryableError
	if errors.As(err, &retryErr) {
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("Teams API temporarily unavailable, please retry").
			WithSendNotice(true).
			WithErrorReason(event.MessageStatusGenericError)
	}
	return err
}

func (c *TeamsClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if !c.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return false, err
	}
	if msg == nil || msg.Content == nil {
		return false, errors.New("missing room name content")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return false, errors.New("missing thread id")
	}
	// Teams group chats use "topic" as the display name.
	if err := c.getAPI().UpdateConversationTopic(ctx, threadID, msg.Content.Name); err != nil {
		return false, err
	}
	return true, nil
}

func (c *TeamsClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if !c.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return false, err
	}
	if msg == nil || msg.Content == nil {
		return false, errors.New("missing room topic content")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return false, errors.New("missing thread id")
	}
	if err := c.getAPI().UpdateConversationTopic(ctx, threadID, msg.Content.Topic); err != nil {
		return false, err
	}
	return true, nil
}

func (c *TeamsClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, errors.New("missing membership change")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return nil, errors.New("missing thread id")
	}

	// Extract the target ghost's network user ID.
	ghost, ok := msg.Target.(*bridgev2.Ghost)
	if !ok || ghost == nil {
		return nil, errors.New("membership change target is not a ghost")
	}
	memberMRI := string(ghost.ID)
	if memberMRI == "" {
		return nil, errors.New("missing target user ID")
	}

	switch msg.Type {
	case bridgev2.Invite:
		if err := c.getAPI().AddMember(ctx, threadID, memberMRI); err != nil {
			return nil, err
		}
	case bridgev2.Kick:
		if err := c.getAPI().RemoveMember(ctx, threadID, memberMRI); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported membership change type: %v", msg.Type)
	}
	return nil, nil
}

func (c *TeamsClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	if !c.IsLoggedIn() {
		return false, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		return false, fmt.Errorf("graph token required for avatar: %w", err)
	}
	if msg == nil || msg.Content == nil {
		return false, errors.New("missing room avatar content")
	}
	threadID := strings.TrimSpace(string(msg.Portal.ID))
	if threadID == "" {
		return false, errors.New("missing thread id")
	}

	mxcURL := string(msg.Content.URL)
	if mxcURL == "" {
		return false, errors.New("missing avatar URL")
	}

	data, err := c.downloadMatrixMedia(ctx, mxcURL, nil)
	if err != nil || len(data) == 0 {
		return false, fmt.Errorf("failed to download avatar: %w", err)
	}

	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return false, err
	}
	if err := gc.SetChatPhoto(ctx, threadID, data); err != nil {
		return false, err
	}
	return true, nil
}

func (c *TeamsClient) markUnread(threadID string) {
	c.unreadMu.Lock()
	defer c.unreadMu.Unlock()
	if c.unreadSeen == nil {
		c.unreadSeen = make(map[string]bool)
	}
	if c.unreadSent == nil {
		c.unreadSent = make(map[string]bool)
	}
	c.unreadSeen[threadID] = true
	c.unreadSent[threadID] = false
}
