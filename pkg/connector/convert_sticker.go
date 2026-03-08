package connector

import (
	"context"
	"io"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// convertStickerMessage converts a Teams sticker into a Matrix m.sticker event.
func (c *TeamsClient) convertStickerMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, stickerURL, altText string, msg model.RemoteMessage) *bridgev2.ConvertedMessage {
	if intent == nil || c == nil {
		return nil
	}
	stickerURL = strings.TrimSpace(stickerURL)
	if stickerURL == "" {
		return nil
	}

	roomID := id.RoomID("")
	if portal != nil {
		roomID = portal.MXID
	}

	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil
	}

	skypeToken := ""
	regionAmsURL := ""
	if c.Meta != nil {
		skypeToken = strings.TrimSpace(c.Meta.SkypeToken)
		regionAmsURL = strings.TrimSpace(c.Meta.RegionAmsURL)
	}

	var data []byte
	var contentType string
	var err error

	if strings.Contains(stickerURL, "ams") || strings.Contains(stickerURL, "api.asm.skype.com") {
		downloadURL := stickerURL
		if regionAmsURL != "" {
			downloadURL = rewriteAMSURL(downloadURL, regionAmsURL)
		}
		data, contentType, err = downloadAMSImage(ctx, httpClient, downloadURL, skypeToken)
	}

	if len(data) == 0 || err != nil {
		data, contentType, err = downloadDirectHTTP(ctx, httpClient, stickerURL)
	}

	if err != nil || len(data) == 0 {
		return nil
	}

	mimeType := detectMIMEType("", contentType, data)
	filename := "sticker"
	if ext := mimeExtension(mimeType); ext != "" {
		filename += ext
	}

	mxc, file, err := intent.UploadMedia(ctx, roomID, data, filename, mimeType)
	if err != nil {
		return nil
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    altText,
		URL:     mxc,
		File:    file,
		Info: &event.FileInfo{
			MimeType: mimeType,
			Size:     len(data),
		},
	}

	extra := perMessageExtra(msg)

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventSticker,
			Content: content,
			Extra:   extra,
		}},
	}
}

// downloadDirectHTTP downloads content from a plain HTTP(S) URL (no auth).
func downloadDirectHTTP(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
