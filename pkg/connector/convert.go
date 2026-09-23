package connector

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	internalbridge "go.mau.fi/mautrix-teams/internal/bridge"
	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// withSubject puts a channel post's title, when it has one, above the body
// in bold, so an announcement reads as it does in Teams.
func withSubject(msg model.RemoteMessage) model.RemoteMessage {
	subject := model.ExtractSubject(msg.PropertiesRaw)
	if subject == "" {
		return msg
	}
	body := strings.TrimSpace(msg.Body)
	formatted := strings.TrimSpace(msg.FormattedBody)
	if formatted == "" && body != "" {
		formatted = plainTextToHTML(body)
	}
	if body != "" {
		msg.Body = subject + "\n\n" + body
	} else {
		msg.Body = subject
	}
	if formatted != "" {
		msg.FormattedBody = "<strong>" + html.EscapeString(subject) + "</strong><br><br>" + formatted
	} else {
		msg.FormattedBody = "<strong>" + html.EscapeString(subject) + "</strong>"
	}
	return msg
}

// errDeletedMessage is returned for a message Teams has deleted: it still
// lists, with an empty body, and must not be bridged.
var errDeletedMessage = errors.New("message was deleted on Teams")

func (c *TeamsClient) convertTeamsMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg model.RemoteMessage) (*bridgev2.ConvertedMessage, error) {
	log := c.log()
	log.Trace().
		Str("message_id", msg.MessageID).
		Str("message_type", msg.MessageType).
		Str("sender_id", msg.SenderID).
		Int("inline_images", len(msg.InlineImages)).
		Bool("has_properties_files", msg.PropertiesFiles != "" && msg.PropertiesFiles != "[]").
		Int("body_len", len(msg.Body)).
		Int("formatted_body_len", len(msg.FormattedBody)).
		Msg("Converting Teams message")

	if model.IsDeleted(msg.PropertiesRaw) {
		return nil, errDeletedMessage
	}
	msg = withSubject(msg)

	// Check for call/meeting system events first.
	if cm := convertCallOrMeetingEvent(msg); cm != nil {
		log.Debug().Str("message_id", msg.MessageID).Msg("Converted as call/meeting event")
		return cm, nil
	}

	// Check for sticker messages before normal processing.
	if stickerURL, altText, ok := model.ParseStickerFromHTML(msg.FormattedBody); ok {
		if cm := c.convertStickerMessage(ctx, portal, intent, stickerURL, altText, msg); cm != nil {
			log.Debug().Str("message_id", msg.MessageID).Msg("Converted as sticker")
			return cm, nil
		}
	}

	attachments, _ := model.ParseAttachments(msg.PropertiesFiles)
	hasDriveItemID := false
	for _, att := range attachments {
		if strings.TrimSpace(att.DriveItemID) != "" {
			hasDriveItemID = true
			break
		}
	}
	hasInlineImages := len(msg.InlineImages) > 0
	hasGIFs := len(msg.GIFs) > 0
	if (!hasDriveItemID && !hasInlineImages && !hasGIFs) || intent == nil || c == nil {
		log.Debug().
			Str("message_id", msg.MessageID).
			Bool("has_drive_item", hasDriveItemID).
			Bool("has_inline_images", hasInlineImages).
			Bool("has_gifs", hasGIFs).
			Bool("intent_nil", intent == nil).
			Msg("Using legacy conversion path")
		return c.convertTeamsMessageLegacy(msg), nil
	}

	extra := perMessageExtra(msg)

	roomID := id.RoomID("")
	if portal != nil {
		roomID = portal.MXID
	}
	mediaParts, fallback := c.reuploadInboundAttachments(ctx, roomID, intent, attachments, extra)
	parts := make([]*bridgev2.ConvertedMessagePart, 0, len(mediaParts)+len(msg.InlineImages)+len(msg.GIFs)+1)
	parts = append(parts, mediaParts...)

	// Re-upload inline images (pasted images in Teams HTML body).
	inlineParts := c.reuploadInlineImages(ctx, roomID, intent, msg.InlineImages, extra)
	parts = append(parts, inlineParts...)

	// GIF-picker GIFs are public CDN files: show them as images, and as
	// links only when the download fails.
	gifParts, gifFallback := c.reuploadGIFs(ctx, roomID, intent, msg.GIFs, extra)
	parts = append(parts, gifParts...)

	// Caption: always preserve Teams message body (and include any GIFs and fallback attachment lines)
	// as a separate m.text message after all attachment parts.
	captionRendered := renderInboundMessageWithGIFs(msg.Body, msg.FormattedBody, fallback, gifFallback)
	if captionPart := buildCaptionPart(networkid.PartID("caption"), captionRendered, extra); captionPart != nil {
		parts = append(parts, captionPart)
	}

	if len(parts) == 0 {
		// Hard guarantee: never drop the message entirely, and say what it
		// was rather than leaving a blank line.
		log.Warn().
			Str("message_id", msg.MessageID).
			Str("message_type", msg.MessageType).
			Int("attachments", len(attachments)).
			Int("inline_images", len(msg.InlineImages)).
			Int("gifs", len(msg.GIFs)).
			Msg("Conversion produced zero parts, sending placeholder")
		return &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{unsupportedMessagePart(msg, extra)},
		}, nil
	}

	// Convert Teams @mentions to Matrix mention pills.
	c.applyMentionPills(ctx, parts, msg.Mentions)

	// Add link preview metadata if present.
	if previews := model.ExtractLinkPreviews(msg.PropertiesRaw); len(previews) > 0 {
		previewData := make([]map[string]any, 0, len(previews))
		for _, p := range previews {
			pd := map[string]any{"matched_url": p.URL}
			if p.Title != "" {
				pd["og:title"] = p.Title
			}
			if p.Description != "" {
				pd["og:description"] = p.Description
			}
			if p.ImageURL != "" {
				pd["og:image"] = p.ImageURL
			}
			if p.SiteName != "" {
				pd["og:site_name"] = p.SiteName
			}
			previewData = append(previewData, pd)
		}
		// Attach to the last text part's Extra.
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i].Content != nil && parts[i].Content.MsgType == event.MsgText {
				if parts[i].Extra == nil {
					parts[i].Extra = make(map[string]any)
				}
				parts[i].Extra["com.beeper.linkpreviews"] = previewData
				break
			}
		}
	}

	cm := &bridgev2.ConvertedMessage{Parts: parts}
	// Channel threaded replies: hand the Teams root message ID to bridgev2,
	// which resolves it to the real Matrix event ID (or a deterministic one
	// during backfill) and emits the m.thread relation itself.
	if threadRoot := strings.TrimSpace(msg.ThreadRootID); threadRoot != "" && threadRoot != strings.TrimSpace(msg.MessageID) {
		root := networkid.MessageID(threadRoot)
		cm.ThreadRoot = &root
	}
	if replyTo := strings.TrimSpace(msg.ReplyToID); replyTo != "" {
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: networkid.MessageID(replyTo),
		}
	}
	return cm, nil
}

func (c *TeamsClient) convertTeamsEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, msg model.RemoteMessage) (*bridgev2.ConvertedEdit, error) {
	if len(existing) == 0 {
		return nil, fmt.Errorf("no existing message parts to edit")
	}

	body := strings.TrimSpace(msg.Body)
	if body == "" {
		body = " "
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if formatted := strings.TrimSpace(msg.FormattedBody); formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
	}

	// Edit the first text part (usually "caption" or the only part).
	var targetPart *database.Message
	for _, part := range existing {
		targetPart = part
		break
	}

	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{{
			Part:    targetPart,
			Type:    event.EventMessage,
			Content: content,
		}},
	}, nil
}

func (c *TeamsClient) convertTeamsMessageLegacy(msg model.RemoteMessage) *bridgev2.ConvertedMessage {
	attachments, _ := model.ParseAttachments(msg.PropertiesFiles)
	rendered := renderInboundMessageWithGIFs(msg.Body, msg.FormattedBody, attachments, msg.GIFs)

	body := strings.TrimSpace(rendered.Body)
	if body == "" && strings.TrimSpace(rendered.FormattedBody) == "" {
		// Try adaptive cards before falling back to empty body.
		if cards := model.ExtractAdaptiveCards(msg.PropertiesRaw); len(cards) > 0 {
			var cardTexts []string
			var cardHTMLs []string
			for _, card := range cards {
				if text := card.RenderPlainText(); text != "" {
					cardTexts = append(cardTexts, text)
				}
				if h := card.RenderHTML(); h != "" {
					cardHTMLs = append(cardHTMLs, h)
				}
			}
			if len(cardTexts) > 0 {
				body = strings.Join(cardTexts, "\n\n")
				rendered.FormattedBody = strings.Join(cardHTMLs, "")
			}
		}
	}
	if body == "" && strings.TrimSpace(rendered.FormattedBody) == "" {
		// A file shared by link: Teams shows a file card and no text.
		if links := model.ExtractSafeLinks(msg.PropertiesRaw); len(links) > 0 {
			lines := make([]string, 0, len(links))
			items := make([]string, 0, len(links))
			for _, link := range links {
				lines = append(lines, "Attachment: "+link)
				items = append(items, fmt.Sprintf("<li>Attachment: <a href=\"%s\">%s</a></li>", html.EscapeString(link), html.EscapeString(link)))
			}
			body = strings.Join(lines, "\n")
			rendered.FormattedBody = "<ul>" + strings.Join(items, "") + "</ul>"
		}
	}
	extra := perMessageExtraWithRendered(msg, rendered)
	if body == "" && strings.TrimSpace(rendered.FormattedBody) == "" {
		log := c.log()
		log.Warn().
			Str("message_id", msg.MessageID).
			Str("message_type", msg.MessageType).
			Msg("Message has no renderable content, sending placeholder")
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{unsupportedMessagePart(msg, extra)}}
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if formatted := strings.TrimSpace(rendered.FormattedBody); formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
		if content.Body == "" {
			// Provide a slightly better fallback for clients that don't support HTML.
			content.Body = stripHTMLFallback(formatted)
			if strings.TrimSpace(content.Body) == "" {
				content.Body = " "
			}
		}
	}

	parts := []*bridgev2.ConvertedMessagePart{{
		Type:    event.EventMessage,
		Content: content,
		Extra:   extra,
	}}
	c.applyMentionPills(context.Background(), parts, msg.Mentions)

	cm := &bridgev2.ConvertedMessage{Parts: parts}
	if replyTo := strings.TrimSpace(msg.ReplyToID); replyTo != "" {
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: networkid.MessageID(replyTo),
		}
	}
	return cm
}

func (c *TeamsClient) reuploadInboundAttachments(
	ctx context.Context,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	attachments []model.TeamsAttachment,
	extra map[string]any,
) (mediaParts []*bridgev2.ConvertedMessagePart, fallback []model.TeamsAttachment) {
	log := c.log()
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil, attachments
	}
	log.Debug().Int("attachments", len(attachments)).Msg("Processing inbound attachments")

	skypeToken := ""
	regionAmsURL := ""
	if c.Meta != nil {
		skypeToken = strings.TrimSpace(c.skypeToken())
		regionAmsURL = strings.TrimSpace(c.Meta.RegionAmsURL)
	}

	// Try to get Graph client for DriveItemID-based downloads.
	var gc *graph.GraphClient
	if err := c.ensureValidGraphToken(ctx); err == nil {
		if graphToken, err := c.Meta.GetGraphAccessToken(); err == nil {
			gc = graph.NewClient(httpClient)
			gc.AccessToken = graphToken
			gc.MaxUploadSize = internalbridge.MaxAttachmentBytesV0
			if c.Login != nil {
				gc.Log = &c.Login.Log
			}
		}
	}

	for i, att := range attachments {
		driveItemID := strings.TrimSpace(att.DriveItemID)
		if driveItemID == "" {
			// No DriveItemID: try AMS download via DownloadURL if available.
			downloadURL := strings.TrimSpace(att.DownloadURL)
			if downloadURL != "" && skypeToken != "" {
				if regionAmsURL != "" {
					downloadURL = rewriteAMSURL(downloadURL, regionAmsURL)
				}
				data, contentType, dlErr := downloadAMSImage(ctx, httpClient, downloadURL, skypeToken)
				if dlErr == nil && len(data) > 0 {
					mimeType := detectMIMEType(att.Filename, contentType, data)
					msgType := matrixMsgTypeForMIME(mimeType)
					mxc, file, upErr := intent.UploadMedia(ctx, roomID, data, strings.TrimSpace(att.Filename), mimeType)
					if upErr == nil {
						part := &bridgev2.ConvertedMessagePart{
							ID:      networkid.PartID(fmt.Sprintf("att_%d", i)),
							Type:    event.EventMessage,
							Extra:   cloneExtra(extra),
							Content: buildMediaContent(msgType, strings.TrimSpace(att.Filename), mimeType, len(data), mxc, file),
						}
						mediaParts = append(mediaParts, part)
						continue
					}
				}
			}
			fallback = append(fallback, att)
			continue
		}

		if gc == nil {
			fallback = append(fallback, att)
			continue
		}
		content, err := gc.DownloadDriveItemContent(ctx, driveItemID)
		if err != nil || content == nil || len(content.Bytes) == 0 {
			fallback = append(fallback, att)
			continue
		}

		mimeType := detectMIMEType(att.Filename, content.ContentType, content.Bytes)
		msgType := matrixMsgTypeForMIME(mimeType)

		mxc, file, err := intent.UploadMedia(ctx, roomID, content.Bytes, strings.TrimSpace(att.Filename), mimeType)
		if err != nil {
			fallback = append(fallback, att)
			continue
		}

		part := &bridgev2.ConvertedMessagePart{
			ID:      networkid.PartID(fmt.Sprintf("att_%d", i)),
			Type:    event.EventMessage,
			Extra:   cloneExtra(extra),
			Content: buildMediaContent(msgType, strings.TrimSpace(att.Filename), mimeType, len(content.Bytes), mxc, file),
		}
		applyVoiceMessageHint(part, att.Filename, mimeType)
		mediaParts = append(mediaParts, part)
	}

	return mediaParts, fallback
}

// applyVoiceMessageHint sets the MSC3245 voice message extension on audio parts
// that appear to be Teams voice messages based on filename pattern.
func applyVoiceMessageHint(part *bridgev2.ConvertedMessagePart, filename string, mimeType string) {
	if part == nil || part.Content == nil {
		return
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "audio/") {
		return
	}
	lower := strings.ToLower(filename)
	if !strings.Contains(lower, "voice") && !strings.Contains(lower, "audio_message") &&
		!strings.HasSuffix(lower, ".ogg") {
		return
	}
	part.Content.MsgType = event.MsgAudio
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	part.Extra["org.matrix.msc3245.voice"] = map[string]any{}
}

func (c *TeamsClient) reuploadInlineImages(
	ctx context.Context,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	images []model.TeamsInlineImage,
	extra map[string]any,
) []*bridgev2.ConvertedMessagePart {
	if len(images) == 0 || c == nil || c.Meta == nil {
		return nil
	}
	skypeToken := strings.TrimSpace(c.skypeToken())
	if skypeToken == "" {
		return nil
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil
	}

	// Rewrite AMS URLs to use enterprise region endpoint when available.
	regionAmsURL := strings.TrimSpace(c.Meta.RegionAmsURL)

	log := c.Login.Log
	var parts []*bridgev2.ConvertedMessagePart
	for i, img := range images {
		imageURL := img.URL
		var data []byte
		var contentType string
		var err error
		if isAMSURL(imageURL, regionAmsURL) {
			// Only AMS gets the region rewrite and the skypetoken; any other
			// host is a public image and must not see the token.
			if regionAmsURL != "" {
				imageURL = rewriteAMSURL(imageURL, regionAmsURL)
			}
			log.Debug().Str("url", imageURL).Msg("Downloading inline image from AMS")
			data, contentType, err = downloadAMSImage(ctx, httpClient, imageURL, skypeToken)
		} else {
			log.Debug().Str("url", imageURL).Msg("Downloading inline image")
			data, contentType, err = downloadPublicImage(ctx, httpClient, imageURL)
		}
		if err != nil {
			log.Err(err).Str("url", img.URL).Msg("Failed to download inline image")
			continue
		}
		if len(data) == 0 {
			log.Warn().Str("url", img.URL).Msg("AMS download returned empty data")
			continue
		}
		log.Debug().Str("url", img.URL).Int("size", len(data)).Str("content_type", contentType).Msg("Downloaded inline image")

		mimeType := detectMIMEType("", contentType, data)
		filename := fmt.Sprintf("inline_image_%d", i)
		if ext := mimeExtension(mimeType); ext != "" {
			filename += ext
		}

		mxc, file, err := intent.UploadMedia(ctx, roomID, data, filename, mimeType)
		if err != nil {
			log.Err(err).Str("url", img.URL).Msg("Failed to upload inline image to Matrix")
			continue
		}

		part := &bridgev2.ConvertedMessagePart{
			ID:      networkid.PartID(fmt.Sprintf("inline_%d", i)),
			Type:    event.EventMessage,
			Extra:   cloneExtra(extra),
			Content: buildMediaContent(event.MsgImage, filename, mimeType, len(data), mxc, file),
		}
		parts = append(parts, part)
	}
	return parts
}

// reuploadGIFs downloads GIF-picker GIFs (public CDN files, fetched without
// any Teams credential) and uploads them as images.  GIFs that cannot be
// fetched are returned so the caption can link them instead.
func (c *TeamsClient) reuploadGIFs(
	ctx context.Context,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	gifs []model.TeamsGIF,
	extra map[string]any,
) (parts []*bridgev2.ConvertedMessagePart, fallback []model.TeamsGIF) {
	if len(gifs) == 0 || c == nil {
		return nil, nil
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil || intent == nil {
		return nil, gifs
	}
	log := c.log()
	for i, gif := range gifs {
		data, contentType, err := downloadPublicImage(ctx, httpClient, gif.URL)
		if err != nil || len(data) == 0 {
			log.Warn().Err(err).Str("url", gif.URL).Msg("Failed to download GIF, linking it instead")
			fallback = append(fallback, gif)
			continue
		}
		mimeType := detectMIMEType("", contentType, data)
		filename := fmt.Sprintf("gif_%d", i)
		if ext := mimeExtension(mimeType); ext != "" {
			filename += ext
		}
		mxc, file, err := intent.UploadMedia(ctx, roomID, data, filename, mimeType)
		if err != nil {
			log.Warn().Err(err).Str("url", gif.URL).Msg("Failed to upload GIF, linking it instead")
			fallback = append(fallback, gif)
			continue
		}
		content := buildMediaContent(event.MsgImage, filename, mimeType, len(data), mxc, file)
		if title := strings.TrimSpace(gif.Title); title != "" && title != "GIF" {
			content.Body = title
		}
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			ID:      networkid.PartID(fmt.Sprintf("gif_%d", i)),
			Type:    event.EventMessage,
			Extra:   cloneExtra(extra),
			Content: content,
		})
	}
	return parts, fallback
}

// unsupportedMessagePart is what a Teams message the converter could get
// nothing out of becomes: a notice naming the message type, so the gap is
// visible and diagnosable rather than a blank line.
func unsupportedMessagePart(msg model.RemoteMessage, extra map[string]any) *bridgev2.ConvertedMessagePart {
	msgType := strings.TrimSpace(msg.MessageType)
	if msgType == "" {
		msgType = "unknown type"
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("[Unsupported Teams message: %s]", msgType),
		},
		Extra: extra,
	}
}

var matrixMentionPillRe = regexp.MustCompile(`<a\s+href="https://matrix\.to/#/([@!][^"]+)">([^<]+)</a>`)

// ghostResolver abstracts ghost MXID lookup for testability.
type ghostResolver interface {
	// resolveGhostMXID returns the Matrix user ID for a Teams user ID, or "" if not found.
	resolveGhostMXID(ctx context.Context, teamsUserID string) id.UserID
	// parseGhostMXID returns the network user ID for a Matrix user ID, or "" if not a ghost.
	parseGhostMXID(mxid id.UserID) (networkid.UserID, bool)
}

// bridgeGhostResolver implements ghostResolver using a real bridgev2.Bridge.
type bridgeGhostResolver struct {
	bridge *bridgev2.Bridge
}

func (r *bridgeGhostResolver) resolveGhostMXID(ctx context.Context, teamsUserID string) id.UserID {
	ghost, err := r.bridge.GetGhostByID(ctx, teamsUserIDToNetworkUserID(teamsUserID))
	if err != nil || ghost == nil {
		return ""
	}
	return ghost.Intent.GetMXID()
}

func (r *bridgeGhostResolver) parseGhostMXID(mxid id.UserID) (networkid.UserID, bool) {
	return r.bridge.Matrix.ParseGhostMXID(mxid)
}

func (c *TeamsClient) getGhostResolver() ghostResolver {
	if c == nil || c.Main == nil || c.Main.Bridge == nil {
		return nil
	}
	return &bridgeGhostResolver{bridge: c.Main.Bridge}
}

// groupSplitMentions joins the spans Teams makes of one mention.  A name
// with spaces arrives as one span per word ("Ethan", "Patrick", "Santee"),
// consecutive item IDs for the same user, joined in the text by
// non-breaking spaces; the pill should cover the whole name.
func groupSplitMentions(mentions []model.TeamsMention) []model.TeamsMention {
	var out []model.TeamsMention
	for _, m := range mentions {
		if n := len(out); n > 0 && out[n-1].UserID != "" && out[n-1].UserID == m.UserID && consecutiveItemIDs(out[n-1].ItemID, m.ItemID) {
			out[n-1].DisplayName += "\u00a0" + m.DisplayName
			out[n-1].ItemID = m.ItemID
			continue
		}
		out = append(out, m)
	}
	return out
}

func consecutiveItemIDs(prev, next string) bool {
	a, errA := strconv.Atoi(strings.TrimSpace(prev))
	b, errB := strconv.Atoi(strings.TrimSpace(next))
	return errA == nil && errB == nil && b == a+1
}

// applyMentionPills replaces Teams @mention display names in message parts
// with Matrix mention pills (<a href="https://matrix.to/#/@ghost:domain">@Name</a>).
func (c *TeamsClient) applyMentionPills(ctx context.Context, parts []*bridgev2.ConvertedMessagePart, mentions []model.TeamsMention) {
	resolver := c.getGhostResolver()
	applyMentionPillsWithResolver(ctx, parts, mentions, resolver)
}

func applyMentionPillsWithResolver(ctx context.Context, parts []*bridgev2.ConvertedMessagePart, mentions []model.TeamsMention, resolver ghostResolver) {
	if len(mentions) == 0 || resolver == nil {
		return
	}
	// Build replacement map: displayName -> Matrix pill HTML.
	type pillInfo struct {
		mxid        id.UserID
		displayName string
	}
	var pills []pillInfo
	for _, group := range groupSplitMentions(mentions) {
		if strings.TrimSpace(group.UserID) == "" || strings.TrimSpace(group.DisplayName) == "" {
			continue
		}
		mxid := resolver.resolveGhostMXID(ctx, group.UserID)
		if mxid == "" {
			continue
		}
		pills = append(pills, pillInfo{mxid: mxid, displayName: group.DisplayName})
	}
	if len(pills) == 0 {
		return
	}

	for _, part := range parts {
		if part == nil || part.Content == nil {
			continue
		}
		for _, pill := range pills {
			pillHTML := fmt.Sprintf(`<a href="https://matrix.to/#/%s">@%s</a>`,
				html.EscapeString(string(pill.mxid)),
				html.EscapeString(pill.displayName))

			escapedName := html.EscapeString(pill.displayName)
			spacedName := strings.ReplaceAll(escapedName, "\u00a0", " ")
			// Replace in FormattedBody (HTML-escaped display name).
			if part.Content.FormattedBody != "" {
				part.Content.FormattedBody = strings.ReplaceAll(part.Content.FormattedBody, escapedName, pillHTML)
				part.Content.FormattedBody = strings.ReplaceAll(part.Content.FormattedBody, spacedName, pillHTML)
				if part.Content.Format == "" {
					part.Content.Format = event.FormatHTML
				}
			} else if part.Content.Body != "" {
				// No formatted body yet — create one with pills.
				part.Content.FormattedBody = html.EscapeString(part.Content.Body)
				part.Content.FormattedBody = strings.ReplaceAll(part.Content.FormattedBody, escapedName, pillHTML)
				part.Content.FormattedBody = strings.ReplaceAll(part.Content.FormattedBody, spacedName, pillHTML)
				part.Content.Format = event.FormatHTML
			}
		}
	}
}

// convertMatrixMentionsToTeams converts Matrix mention pills in HTML to Teams
// mention format: <span itemtype="http://schema.skype.com/Mention" itemid="INDEX">@Name</span>
// and returns the modified body plus a JSON-serializable mentions properties array.
// plaintextToTeamsHTML escapes a plain-text message and wraps it as Teams HTML,
// mirroring the Teams web client (newlines become <br>, wrapped in <p>).
func plaintextToTeamsHTML(text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	escaped := html.EscapeString(normalized)
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return "<p>" + escaped + "</p>"
}

// mxReplyRe matches Matrix's rich-reply fallback block, which Teams does not
// understand (the bridge sends replies via its own reply mechanism).
var mxReplyRe = regexp.MustCompile(`(?is)<mx-reply>.*?</mx-reply>`)

// underlineSpanRe matches the underline span that Org (ement's send filter)
// and some Matrix clients emit. Teams has no stylesheet, so class="underline"
// is invisible; it must become a <u> tag.
var underlineSpanRe = regexp.MustCompile(`(?is)<span class="underline">(.*?)</span>`)

// matrixHTMLToTeamsHTML adapts a Matrix formatted_body into Teams HTML. Matrix,
// Org and Teams share a tag vocabulary (b/strong, i/em, u, del/s, code, pre, a,
// ul/ol/li, blockquote, br, p), so the markup passes through; only constructs
// Teams cannot render are converted:
//   - the mx-reply fallback block is stripped (replies use Teams' own mechanism)
//   - <span class="underline"> becomes <u> (Teams has no CSS for the class)
//
// Links, lists, code blocks and blockquotes already render in Teams as-is.
func matrixHTMLToTeamsHTML(formattedBody string) string {
	out := mxReplyRe.ReplaceAllString(formattedBody, "")
	out = underlineSpanRe.ReplaceAllString(out, "<u>$1</u>")
	return strings.TrimSpace(out)
}

func (c *TeamsClient) convertMatrixMentionsToTeams(body string) (string, []map[string]any) {
	resolver := c.getGhostResolver()
	return convertMatrixMentionsToTeamsWithResolver(body, resolver)
}

// convertCallOrMeetingEvent checks if a message is a call or meeting system event
// and converts it to a Matrix m.notice if so.
func convertCallOrMeetingEvent(msg model.RemoteMessage) *bridgev2.ConvertedMessage {
	msgType := strings.ToLower(msg.MessageType)
	var noticeBody, noticeHTML string

	switch {
	case strings.HasPrefix(msgType, "event/call"):
		switch {
		case strings.Contains(msgType, "missed"):
			senderName := strings.TrimSpace(msg.SenderName)
			if senderName == "" {
				senderName = "Someone"
			}
			noticeBody = fmt.Sprintf("Missed call from %s", senderName)
		case strings.Contains(msgType, "end"):
			noticeBody = "Call ended"
		case strings.Contains(msgType, "start"):
			noticeBody = "Call started"
		default:
			noticeBody = "Call event"
		}
	case msgType == "event/call":
		noticeBody = "Call event"
	default:
		// Check for meeting join URLs in body.
		meetingURL := extractMeetingJoinURL(msg.Body)
		if meetingURL == "" {
			meetingURL = extractMeetingJoinURL(msg.FormattedBody)
		}
		if meetingURL != "" {
			noticeBody = fmt.Sprintf("Meeting: %s", meetingURL)
			noticeHTML = fmt.Sprintf(`Meeting: <a href="%s">Join</a>`,
				html.EscapeString(meetingURL))
		}
	}

	if noticeBody == "" {
		return nil
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    noticeBody,
	}
	if noticeHTML != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = noticeHTML
	}
	extra := perMessageExtra(msg)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: content,
			Extra:   extra,
		}},
	}
}

// extractMeetingJoinURL extracts a Teams meeting join URL from a message body.
func extractMeetingJoinURL(body string) string {
	idx := strings.Index(body, "https://teams.microsoft.com/l/meetup-join/")
	if idx < 0 {
		return ""
	}
	end := idx
	for end < len(body) && body[end] != ' ' && body[end] != '"' && body[end] != '<' && body[end] != '\n' {
		end++
	}
	return body[idx:end]
}

func convertMatrixMentionsToTeamsWithResolver(body string, resolver ghostResolver) (string, []map[string]any) {
	if resolver == nil {
		return body, nil
	}
	matches := matrixMentionPillRe.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return body, nil
	}

	var mentions []map[string]any
	var result strings.Builder
	lastEnd := 0
	mentionIdx := 0

	for _, match := range matches {
		// match[0:1] = full match start/end
		// match[2:3] = MXID group
		// match[4:5] = display name group
		fullStart, fullEnd := match[0], match[1]
		mxidStr := body[match[2]:match[3]]
		displayName := body[match[4]:match[5]]

		mxid := id.UserID(html.UnescapeString(mxidStr))
		ghostID, isGhost := resolver.parseGhostMXID(mxid)
		if !isGhost {
			// Not a bridge ghost — leave the pill as-is.
			continue
		}
		// Convert ghost network ID back to Teams MRI.
		teamsMRI := string(ghostID)

		result.WriteString(body[lastEnd:fullStart])
		result.WriteString(fmt.Sprintf(
			`<span itemtype="http://schema.skype.com/Mention" itemid="%d">@%s</span>`,
			mentionIdx, html.EscapeString(displayName),
		))
		mentions = append(mentions, map[string]any{
			"id":          mentionIdx,
			"mri":         teamsMRI,
			"displayName": html.UnescapeString(displayName),
		})
		mentionIdx++
		lastEnd = fullEnd
	}

	if len(mentions) == 0 {
		return body, nil
	}
	result.WriteString(body[lastEnd:])
	return result.String(), mentions
}
