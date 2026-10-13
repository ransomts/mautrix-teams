package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	internalbridge "go.mau.fi/mautrix-teams/internal/bridge"
	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// attachmentGraph is the part of the Graph client that fetches attachments.
type attachmentGraph interface {
	DownloadSharedItemContent(ctx context.Context, shareURL string) (*graph.DriveItemContent, error)
	DownloadSharedItemThumbnail(ctx context.Context, shareURL string) (*graph.DriveItemContent, error)
	DownloadDriveItemContent(ctx context.Context, driveItemID string) (*graph.DriveItemContent, error)
}

// attachmentSources are the ways an attachment can be fetched; either may be
// nil when its token is missing.
type attachmentSources struct {
	graph attachmentGraph
	// ams downloads an AMS URL with the skypetoken.
	ams func(ctx context.Context, url string) ([]byte, string, error)
}

var errNoAttachmentSource = errors.New("no way to download the attachment")

// fetch downloads an attachment's bytes.  The sharing link comes first: it
// reaches files in anyone's OneDrive or SharePoint that the user can open,
// whereas the drive item ID only resolves in the user's own OneDrive.
func (s attachmentSources) fetch(ctx context.Context, att model.TeamsAttachment) (data []byte, contentType string, err error) {
	err = errNoAttachmentSource
	if s.graph != nil {
		if shareURL := strings.TrimSpace(att.ShareURL); shareURL != "" {
			content, shareErr := s.graph.DownloadSharedItemContent(ctx, shareURL)
			if shareErr == nil && content != nil && len(content.Bytes) > 0 {
				return content.Bytes, content.ContentType, nil
			}
			err = fmt.Errorf("via share link: %w", orEmpty(shareErr))
		}
		if driveItemID := strings.TrimSpace(att.DriveItemID); driveItemID != "" {
			content, itemErr := s.graph.DownloadDriveItemContent(ctx, driveItemID)
			if itemErr == nil && content != nil && len(content.Bytes) > 0 {
				return content.Bytes, content.ContentType, nil
			}
			err = errors.Join(err, fmt.Errorf("via drive item: %w", orEmpty(itemErr)))
		}
	}
	if s.ams != nil && strings.TrimSpace(att.DriveItemID) == "" {
		if downloadURL := strings.TrimSpace(att.DownloadURL); downloadURL != "" {
			data, contentType, amsErr := s.ams(ctx, downloadURL)
			if amsErr == nil && len(data) > 0 {
				return data, contentType, nil
			}
			err = errors.Join(err, fmt.Errorf("via AMS: %w", orEmpty(amsErr)))
		}
	}
	return nil, "", err
}

// thumbnail downloads Graph's preview picture of an attachment, or returns
// nil if there is none.
func (s attachmentSources) thumbnail(ctx context.Context, att model.TeamsAttachment) *graph.DriveItemContent {
	shareURL := strings.TrimSpace(att.ShareURL)
	if s.graph == nil || shareURL == "" {
		return nil
	}
	content, err := s.graph.DownloadSharedItemThumbnail(ctx, shareURL)
	if err != nil || content == nil || len(content.Bytes) == 0 {
		return nil
	}
	return content
}

var errEmptyDownload = errors.New("empty response")

func orEmpty(err error) error {
	if err == nil {
		return errEmptyDownload
	}
	return err
}

func (c *TeamsClient) attachmentSources(ctx context.Context) attachmentSources {
	var sources attachmentSources
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return sources
	}
	if err := c.ensureValidGraphToken(ctx); err == nil {
		if graphToken, err := c.graphAccessToken(); err == nil {
			gc := graph.NewClient(httpClient)
			gc.AccessToken = graphToken
			gc.MaxUploadSize = internalbridge.MaxAttachmentBytesV0
			if c.Login != nil {
				gc.Log = &c.Login.Log
			}
			sources.graph = gc
		}
	}
	if c.Meta != nil {
		if skypeToken := strings.TrimSpace(c.skypeToken()); skypeToken != "" {
			regionAmsURL := c.regionAmsURL()
			sources.ams = func(ctx context.Context, downloadURL string) ([]byte, string, error) {
				if regionAmsURL != "" {
					downloadURL = rewriteAMSURL(downloadURL, regionAmsURL)
				}
				return downloadAMSImage(ctx, httpClient, downloadURL, skypeToken)
			}
		}
	}
	return sources
}

func (c *TeamsClient) reuploadInboundAttachments(
	ctx context.Context,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	attachments []model.TeamsAttachment,
	extra map[string]any,
) (mediaParts []*bridgev2.ConvertedMessagePart, fallback []model.TeamsAttachment) {
	if c.getConsumerHTTP() == nil {
		return nil, attachments
	}
	return c.reuploadAttachmentsFrom(ctx, c.attachmentSources(ctx), roomID, intent, attachments, extra)
}

// reuploadAttachmentsFrom uploads each attachment to Matrix as an image,
// video, audio or file message; a file gets Graph's preview picture as its
// thumbnail.  An attachment that cannot be fetched or uploaded is returned
// in fallback, for the caption to link, and is preceded by its preview
// picture as an image when Graph has one.
func (c *TeamsClient) reuploadAttachmentsFrom(
	ctx context.Context,
	sources attachmentSources,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	attachments []model.TeamsAttachment,
	extra map[string]any,
) (mediaParts []*bridgev2.ConvertedMessagePart, fallback []model.TeamsAttachment) {
	log := c.log()
	for i, att := range attachments {
		filename := strings.TrimSpace(att.Filename)
		part, err := c.reuploadAttachment(ctx, sources, roomID, intent, att, extra)
		if err == nil {
			part.ID = networkid.PartID(fmt.Sprintf("att_%d", i))
			mediaParts = append(mediaParts, part)
			continue
		}
		log.Warn().Err(err).Str("filename", filename).Msg("Attachment not re-uploaded, linking it instead")
		fallback = append(fallback, att)
		if preview := sources.thumbnail(ctx, att); preview != nil {
			mimeType := detectMIMEType("", preview.ContentType, preview.Bytes)
			name := filename + " (preview)" + mimeExtension(mimeType)
			mxc, file, upErr := intent.UploadMedia(ctx, roomID, preview.Bytes, name, mimeType)
			if upErr != nil {
				log.Warn().Err(upErr).Str("filename", filename).Msg("Failed to upload attachment preview")
				continue
			}
			mediaParts = append(mediaParts, &bridgev2.ConvertedMessagePart{
				ID:      networkid.PartID(fmt.Sprintf("att_%d_preview", i)),
				Type:    event.EventMessage,
				Extra:   cloneExtra(extra),
				Content: buildMediaContent(event.MsgImage, name, mimeType, len(preview.Bytes), mxc, file),
			})
		}
	}
	return mediaParts, fallback
}

func (c *TeamsClient) reuploadAttachment(
	ctx context.Context,
	sources attachmentSources,
	roomID id.RoomID,
	intent bridgev2.MatrixAPI,
	att model.TeamsAttachment,
	extra map[string]any,
) (*bridgev2.ConvertedMessagePart, error) {
	filename := strings.TrimSpace(att.Filename)
	data, contentType, err := sources.fetch(ctx, att)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	mimeType := detectMIMEType(filename, contentType, data)
	msgType := matrixMsgTypeForMIME(mimeType)
	mxc, file, err := intent.UploadMedia(ctx, roomID, data, filename, mimeType)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	part := &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Extra:   cloneExtra(extra),
		Content: buildMediaContent(msgType, filename, mimeType, len(data), mxc, file),
	}
	if msgType == event.MsgFile || msgType == event.MsgVideo {
		c.addThumbnail(ctx, sources, roomID, intent, att, part.Content)
	}
	applyVoiceMessageHint(part, filename, mimeType)
	return part, nil
}

// addThumbnail sets Graph's preview picture of att as content's thumbnail,
// if Graph has one; a missing preview is not an error.
func (c *TeamsClient) addThumbnail(ctx context.Context, sources attachmentSources, roomID id.RoomID, intent bridgev2.MatrixAPI, att model.TeamsAttachment, content *event.MessageEventContent) {
	preview := sources.thumbnail(ctx, att)
	if preview == nil {
		return
	}
	mimeType := detectMIMEType("", preview.ContentType, preview.Bytes)
	mxc, file, err := intent.UploadMedia(ctx, roomID, preview.Bytes, "thumbnail"+mimeExtension(mimeType), mimeType)
	if err != nil {
		log := c.log()
		log.Warn().Err(err).Str("filename", att.Filename).Msg("Failed to upload attachment thumbnail")
		return
	}
	content.Info.ThumbnailInfo = &event.FileInfo{MimeType: mimeType, Size: len(preview.Bytes)}
	if file != nil {
		if file.URL == "" {
			file.URL = mxc
		}
		content.Info.ThumbnailFile = file
	} else {
		content.Info.ThumbnailURL = mxc
	}
}
