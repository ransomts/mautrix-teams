package graph

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
)

var ErrEmptyShareURL = errors.New("share url is empty")

// shareID encodes a sharing URL as a Graph share ID: "u!" and the URL in
// unpadded base64url.
func shareID(shareURL string) string {
	return "u!" + base64.RawURLEncoding.EncodeToString([]byte(shareURL))
}

// DownloadSharedItemContent downloads a file through its sharing URL.  Unlike
// DownloadDriveItemContent, which only reaches the user's own OneDrive, this
// works for any file shared with the user, e.g. one a colleague attached to
// a chat, which lives in their OneDrive.
func (c *GraphClient) DownloadSharedItemContent(ctx context.Context, shareURL string) (*DriveItemContent, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return nil, ErrEmptyShareURL
	}
	return c.downloadContent(ctx, "https://graph.microsoft.com/v1.0/shares/"+shareID(shareURL)+"/driveItem/content")
}

// DownloadSharedItemThumbnail downloads the large preview picture Graph
// renders for a shared file: the picture itself, a PDF's first page, a
// deck's first slide, a video frame.  File types Graph cannot render fail
// with a GraphDriveItemContentError.
func (c *GraphClient) DownloadSharedItemThumbnail(ctx context.Context, shareURL string) (*DriveItemContent, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return nil, ErrEmptyShareURL
	}
	return c.downloadContent(ctx, "https://graph.microsoft.com/v1.0/shares/"+shareID(shareURL)+"/driveItem/thumbnails/0/large/content")
}
