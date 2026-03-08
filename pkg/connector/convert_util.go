package connector

// Pure utility functions for message conversion: rendering, MIME detection,
// URL rewriting, and HTML helpers. No TeamsClient dependency.

import (
	"context"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	internalbridge "go.mau.fi/mautrix-teams/internal/bridge"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

type renderedInboundMessage struct {
	Body          string
	FormattedBody string
	Extra         map[string]any
}

func renderInboundMessage(body string, formattedBody string, attachments []model.TeamsAttachment) renderedInboundMessage {
	return renderInboundMessageWithGIFs(body, formattedBody, attachments, nil)
}

func renderInboundMessageWithGIFs(body string, formattedBody string, attachments []model.TeamsAttachment, gifs []model.TeamsGIF) renderedInboundMessage {
	result := renderedInboundMessage{
		Body:          strings.TrimSpace(body),
		FormattedBody: strings.TrimSpace(formattedBody),
	}
	if len(attachments) == 0 && len(gifs) == 0 {
		return result
	}

	sections := make([]string, 0, 3)
	if strings.TrimSpace(result.Body) != "" {
		sections = append(sections, result.Body)
	}
	if len(attachments) > 0 {
		sections = append(sections, renderAttachmentBody(attachments))
	}
	if len(gifs) > 0 {
		sections = append(sections, renderGIFBody(gifs))
	}
	result.Body = strings.Join(sections, "\n\n")

	baseHTML := result.FormattedBody
	if baseHTML == "" && strings.TrimSpace(body) != "" {
		baseHTML = plainTextToHTML(body)
	}
	htmlSections := make([]string, 0, 3)
	if baseHTML != "" {
		htmlSections = append(htmlSections, baseHTML)
	}
	if len(attachments) > 0 {
		htmlSections = append(htmlSections, renderAttachmentHTML(attachments))
	}
	if len(gifs) > 0 {
		htmlSections = append(htmlSections, renderGIFHTML(gifs))
	}
	result.FormattedBody = strings.Join(htmlSections, "<br><br>")
	result.Extra = nil
	return result
}

func stripHTMLFallback(htmlStr string) string {
	out := strings.ReplaceAll(htmlStr, "<br>", "\n")
	out = strings.ReplaceAll(out, "<br/>", "\n")
	out = strings.ReplaceAll(out, "<br />", "\n")
	out = strings.ReplaceAll(out, "<p>", "")
	out = strings.ReplaceAll(out, "</p>", "")
	return out
}

func renderAttachmentBody(attachments []model.TeamsAttachment) string {
	lines := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.ShareURL) == "" {
			lines = append(lines, fmt.Sprintf("Attachment: %s", attachment.Filename))
			continue
		}
		lines = append(lines, fmt.Sprintf("Attachment: %s - %s", attachment.Filename, attachment.ShareURL))
	}
	return strings.Join(lines, "\n")
}

func renderAttachmentHTML(attachments []model.TeamsAttachment) string {
	var b strings.Builder
	b.WriteString("<ul>")
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.ShareURL) == "" {
			b.WriteString("<li>Attachment: ")
			b.WriteString(html.EscapeString(attachment.Filename))
			b.WriteString("</li>")
			continue
		}
		b.WriteString("<li>Attachment: <a href=\"")
		b.WriteString(html.EscapeString(attachment.ShareURL))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(attachment.Filename))
		b.WriteString("</a></li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

func renderGIFBody(gifs []model.TeamsGIF) string {
	lines := make([]string, 0, len(gifs))
	for _, gif := range gifs {
		lines = append(lines, fmt.Sprintf("GIF: %s - %s", gif.Title, gif.URL))
	}
	return strings.Join(lines, "\n")
}

func renderGIFHTML(gifs []model.TeamsGIF) string {
	var b strings.Builder
	b.WriteString("<ul>")
	for _, gif := range gifs {
		b.WriteString("<li>GIF: <a href=\"")
		b.WriteString(html.EscapeString(gif.URL))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(gif.Title))
		b.WriteString("</a></li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

func plainTextToHTML(text string) string {
	escaped := html.EscapeString(text)
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return escaped
}

func detectMIMEType(filename string, headerContentType string, data []byte) string {
	if ct := normalizeContentType(headerContentType); ct != "" && ct != "application/octet-stream" {
		return ct
	}
	if len(data) > 0 {
		if sniffed := normalizeContentType(http.DetectContentType(data)); sniffed != "" {
			return sniffed
		}
	}
	if ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename))); ext != "" {
		if byExt := normalizeContentType(mime.TypeByExtension(ext)); byExt != "" {
			return byExt
		}
	}
	return "application/octet-stream"
}

func normalizeContentType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if semi := strings.IndexByte(value, ';'); semi >= 0 {
		value = strings.TrimSpace(value[:semi])
	}
	return value
}

func matrixMsgTypeForMIME(mimeType string) event.MessageType {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mimeType, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return event.MsgAudio
	default:
		return event.MsgFile
	}
}

func buildMediaContent(
	msgType event.MessageType,
	filename string,
	mimeType string,
	size int,
	mxc id.ContentURIString,
	file *event.EncryptedFileInfo,
) *event.MessageEventContent {
	content := &event.MessageEventContent{
		MsgType:  msgType,
		Body:     filename,
		FileName: filename,
		Info: &event.FileInfo{
			MimeType: mimeType,
			Size:     size,
		},
	}
	if file != nil {
		if file.URL == "" && mxc != "" {
			file.URL = mxc
		}
		content.File = file
	} else {
		content.URL = mxc
	}
	if strings.TrimSpace(content.Body) == "" {
		content.Body = "file"
	}
	return content
}

func buildCaptionPart(partID networkid.PartID, rendered renderedInboundMessage, extra map[string]any) *bridgev2.ConvertedMessagePart {
	body := strings.TrimSpace(rendered.Body)
	formatted := strings.TrimSpace(rendered.FormattedBody)
	if body == "" && formatted == "" {
		return nil
	}
	if body == "" {
		body = " "
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
		if content.Body == " " {
			content.Body = stripHTMLFallback(formatted)
			if strings.TrimSpace(content.Body) == "" {
				content.Body = " "
			}
		}
	}
	return &bridgev2.ConvertedMessagePart{
		ID:      partID,
		Type:    event.EventMessage,
		Content: content,
		Extra:   extra,
	}
}

// rewriteAMSURL replaces the host of a consumer AMS URL (e.g. us-api.asm.skype.com)
// with the enterprise region AMS endpoint.
func rewriteAMSURL(imageURL string, regionAmsBase string) string {
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return imageURL
	}
	regionParsed, err := url.Parse(regionAmsBase)
	if err != nil {
		return imageURL
	}
	parsed.Scheme = regionParsed.Scheme
	parsed.Host = regionParsed.Host
	return parsed.String()
}

func mimeExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ""
	}
}

func perMessageExtra(msg model.RemoteMessage) map[string]any {
	extra := make(map[string]any)
	if senderID := strings.TrimSpace(msg.SenderID); senderID != "" {
		if displayName := strings.TrimSpace(msg.SenderName); displayName != "" {
			extra["com.beeper.per_message_profile"] = map[string]any{
				"id":          senderID,
				"displayname": displayName,
			}
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func perMessageExtraWithRendered(msg model.RemoteMessage, rendered renderedInboundMessage) map[string]any {
	extra := perMessageExtra(msg)
	if len(rendered.Extra) == 0 {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, len(rendered.Extra))
	}
	for k, v := range rendered.Extra {
		extra[k] = v
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func cloneExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func downloadAMSImage(ctx context.Context, httpClient *http.Client, imageURL string, skypeToken string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "skype_token "+skypeToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("AMS download failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	maxSize := int64(internalbridge.MaxAttachmentBytesV0)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxSize {
		return nil, "", fmt.Errorf("inline image exceeds max size")
	}
	return data, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}
