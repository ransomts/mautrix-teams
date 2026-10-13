package connector

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type fakeAttachmentGraph struct {
	shared     map[string]*graph.DriveItemContent
	thumbnails map[string]*graph.DriveItemContent
	items      map[string]*graph.DriveItemContent
}

func lookupContent(m map[string]*graph.DriveItemContent, key string) (*graph.DriveItemContent, error) {
	if content, ok := m[key]; ok {
		return content, nil
	}
	return nil, graph.GraphDriveItemContentError{Status: 404}
}

func (f fakeAttachmentGraph) DownloadSharedItemContent(_ context.Context, shareURL string) (*graph.DriveItemContent, error) {
	return lookupContent(f.shared, shareURL)
}

func (f fakeAttachmentGraph) DownloadSharedItemThumbnail(_ context.Context, shareURL string) (*graph.DriveItemContent, error) {
	return lookupContent(f.thumbnails, shareURL)
}

func (f fakeAttachmentGraph) DownloadDriveItemContent(_ context.Context, driveItemID string) (*graph.DriveItemContent, error) {
	return lookupContent(f.items, driveItemID)
}

// uploadRecorder is a MatrixAPI whose UploadMedia records the uploads; any
// other method panics.
type uploadRecorder struct {
	bridgev2.MatrixAPI
	names []string
	fail  bool
}

func (u *uploadRecorder) UploadMedia(_ context.Context, _ id.RoomID, _ []byte, fileName, _ string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	if u.fail {
		return "", nil, errors.New("too large")
	}
	u.names = append(u.names, fileName)
	return id.ContentURIString("mxc://localhost/" + fileName), nil, nil
}

func TestReuploadAttachmentImageViaShareLink(t *testing.T) {
	// A colleague's file: its drive item is in their OneDrive, which /me
	// cannot reach, so only the sharing link works.
	att := model.TeamsAttachment{Filename: "cat.png", DriveItemID: "theirs", ShareURL: "https://share/cat"}
	sources := attachmentSources{graph: fakeAttachmentGraph{
		shared: map[string]*graph.DriveItemContent{"https://share/cat": {Bytes: pngBytes, ContentType: "image/png"}},
	}}
	intent := &uploadRecorder{}
	parts, fallback := (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), sources, "!r", intent, []model.TeamsAttachment{att}, nil)
	if len(fallback) != 0 || len(parts) != 1 {
		t.Fatalf("got %d parts, %d fallback; want 1, 0", len(parts), len(fallback))
	}
	content := parts[0].Content
	if content.MsgType != event.MsgImage || content.URL != "mxc://localhost/cat.png" || content.Info.ThumbnailURL != "" {
		t.Fatalf("unexpected content: %+v", content)
	}
}

func TestReuploadAttachmentFallsBackToDriveItem(t *testing.T) {
	att := model.TeamsAttachment{Filename: "mine.png", DriveItemID: "mine", ShareURL: "https://share/mine"}
	sources := attachmentSources{graph: fakeAttachmentGraph{
		items: map[string]*graph.DriveItemContent{"mine": {Bytes: pngBytes, ContentType: "image/png"}},
	}}
	parts, fallback := (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), sources, "!r", &uploadRecorder{}, []model.TeamsAttachment{att}, nil)
	if len(fallback) != 0 || len(parts) != 1 || parts[0].Content.MsgType != event.MsgImage {
		t.Fatalf("got %d parts, %d fallback", len(parts), len(fallback))
	}
}

func TestReuploadAttachmentFileGetsThumbnail(t *testing.T) {
	att := model.TeamsAttachment{Filename: "slides.pptx", ShareURL: "https://share/deck"}
	sources := attachmentSources{graph: fakeAttachmentGraph{
		shared:     map[string]*graph.DriveItemContent{"https://share/deck": {Bytes: []byte("PK\x03\x04deck"), ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}},
		thumbnails: map[string]*graph.DriveItemContent{"https://share/deck": {Bytes: pngBytes, ContentType: "image/png"}},
	}}
	intent := &uploadRecorder{}
	parts, _ := (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), sources, "!r", intent, []model.TeamsAttachment{att}, nil)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	content := parts[0].Content
	if content.MsgType != event.MsgFile || content.URL != "mxc://localhost/slides.pptx" {
		t.Fatalf("unexpected content: %+v", content)
	}
	if content.Info.ThumbnailURL != "mxc://localhost/thumbnail.png" || content.Info.ThumbnailInfo == nil || content.Info.ThumbnailInfo.MimeType != "image/png" {
		t.Fatalf("unexpected thumbnail: %+v", content.Info)
	}
}

func TestReuploadAttachmentUnreachableLinksWithPreview(t *testing.T) {
	att := model.TeamsAttachment{Filename: "huge.mp4", ShareURL: "https://share/huge"}
	sources := attachmentSources{graph: fakeAttachmentGraph{
		shared:     map[string]*graph.DriveItemContent{"https://share/huge": {Bytes: []byte("video"), ContentType: "video/mp4"}},
		thumbnails: map[string]*graph.DriveItemContent{"https://share/huge": {Bytes: pngBytes, ContentType: "image/png"}},
	}}
	// The file downloads but is too large to upload; the preview picture
	// fails to upload as well, so only the link is left.
	parts, fallback := (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), sources, "!r", &uploadRecorder{fail: true}, []model.TeamsAttachment{att}, nil)
	if len(parts) != 0 || len(fallback) != 1 {
		t.Fatalf("got %d parts, %d fallback; want 0, 1", len(parts), len(fallback))
	}

	// Download denied: the link stays, preceded by the preview picture.
	sources.graph = fakeAttachmentGraph{
		thumbnails: map[string]*graph.DriveItemContent{"https://share/huge": {Bytes: pngBytes, ContentType: "image/png"}},
	}
	intent := &uploadRecorder{}
	parts, fallback = (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), sources, "!r", intent, []model.TeamsAttachment{att}, nil)
	if len(fallback) != 1 || len(parts) != 1 {
		t.Fatalf("got %d parts, %d fallback; want 1, 1", len(parts), len(fallback))
	}
	if parts[0].ID != "att_0_preview" || parts[0].Content.MsgType != event.MsgImage || parts[0].Content.Body != "huge.mp4 (preview).png" {
		t.Fatalf("unexpected preview part: %s %+v", parts[0].ID, parts[0].Content)
	}
}

func TestReuploadAttachmentNoSources(t *testing.T) {
	att := model.TeamsAttachment{Filename: "a.pdf", ShareURL: "https://share/a"}
	parts, fallback := (&TeamsClient{}).reuploadAttachmentsFrom(context.Background(), attachmentSources{}, "!r", &uploadRecorder{}, []model.TeamsAttachment{att}, nil)
	if len(parts) != 0 || len(fallback) != 1 {
		t.Fatalf("got %d parts, %d fallback; want 0, 1", len(parts), len(fallback))
	}
}

func TestAttachmentFetchUsesAMSWithoutDriveItem(t *testing.T) {
	att := model.TeamsAttachment{Filename: "x.png", DownloadURL: "https://ams/x"}
	sources := attachmentSources{ams: func(_ context.Context, url string) ([]byte, string, error) {
		if url != "https://ams/x" {
			t.Fatalf("unexpected url %s", url)
		}
		return pngBytes, "image/png", nil
	}}
	data, _, err := sources.fetch(context.Background(), att)
	if err != nil || len(data) == 0 {
		t.Fatalf("fetch failed: %v", err)
	}
}
