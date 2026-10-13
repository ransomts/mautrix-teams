package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	teamsclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

var ErrEmptyDownloadDriveItemID = errors.New("drive item id is empty")

type GraphDriveItemContentError struct {
	Status      int
	BodySnippet string
}

func (e GraphDriveItemContentError) Error() string {
	return fmt.Sprintf("graph drive item content request failed: status %d: %s", e.Status, e.BodySnippet)
}

type DriveItemContent struct {
	Bytes       []byte
	ContentType string
}

func (c *GraphClient) DownloadDriveItemContent(ctx context.Context, driveItemID string) (*DriveItemContent, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	driveItemID = strings.TrimSpace(driveItemID)
	if driveItemID == "" {
		return nil, ErrEmptyDownloadDriveItemID
	}

	return c.downloadContent(ctx, "https://graph.microsoft.com/v1.0/me/drive/items/"+url.PathEscape(driveItemID)+"/content")
}

// downloadContent GETs a Graph content endpoint, which redirects to a
// pre-authenticated download URL, and reads the body up to the upload cap.
func (c *GraphClient) downloadContent(ctx context.Context, endpoint string) (*DriveItemContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))

	resp, err := c.executor().Do(ctx, req, classifyDriveItemContentResponse)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	defer resp.Body.Close()

	max := int64(defaultMaxUploadBytes)
	if c.MaxUploadSize > 0 {
		max = int64(c.MaxUploadSize)
	}
	// Read at most max+1 to enforce a hard cap.
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) > max {
		return nil, ErrUploadContentTooLarge
	}

	return &DriveItemContent{
		Bytes:       data,
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
	}, nil
}

func classifyDriveItemContentResponse(resp *http.Response) error {
	if resp == nil {
		return errors.New("missing response")
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return teamsclient.RetryableError{
			Status:     resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return teamsclient.RetryableError{Status: resp.StatusCode}
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
	return GraphDriveItemContentError{Status: resp.StatusCode, BodySnippet: string(snippet)}
}
