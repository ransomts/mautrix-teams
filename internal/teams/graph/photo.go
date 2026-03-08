package graph

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GetUserPhoto downloads a user's profile photo from the Graph API.
// The userID should be the Teams user MRI (e.g. "8:orgid:abc-123") — the
// "8:orgid:" prefix is stripped to get the Azure AD object ID.
// Returns the photo bytes and content type, or an error.
func (c *GraphClient) GetUserPhoto(ctx context.Context, userID string) ([]byte, string, error) {
	if c == nil || c.HTTP == nil {
		return nil, "", ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, "", ErrMissingGraphAccessToken
	}

	objectID := extractObjectID(userID)
	if objectID == "" {
		return nil, "", fmt.Errorf("cannot extract Azure AD object ID from %q", userID)
	}

	endpoint := "https://graph.microsoft.com/v1.0/users/" + objectID + "/photo/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// User has no profile photo set.
		return nil, "", nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, "", fmt.Errorf("graph photo request failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	const maxPhotoSize = 10 * 1024 * 1024 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoSize))
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}

// GetChatPhoto downloads a group chat's photo from the Graph API.
func (c *GraphClient) GetChatPhoto(ctx context.Context, chatID string) ([]byte, string, error) {
	if c == nil || c.HTTP == nil {
		return nil, "", ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, "", ErrMissingGraphAccessToken
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, "", fmt.Errorf("missing chat ID")
	}

	endpoint := "https://graph.microsoft.com/v1.0/chats/" + chatID + "/photo/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, "", nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, "", fmt.Errorf("graph chat photo request failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	const maxPhotoSize = 10 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoSize))
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}

// SetChatPhoto uploads a new photo for a group chat via the Graph API.
func (c *GraphClient) SetChatPhoto(ctx context.Context, chatID string, data []byte) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return ErrMissingGraphAccessToken
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("missing chat ID")
	}

	endpoint := "https://graph.microsoft.com/v1.0/chats/" + chatID + "/photo/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Content-Type", "image/jpeg")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return fmt.Errorf("set chat photo failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

// extractObjectID strips known MRI prefixes to get the Azure AD object ID.
func extractObjectID(userID string) string {
	id := strings.TrimSpace(userID)
	// Common prefixes: "8:orgid:", "8:live:", "8:"
	if idx := strings.LastIndex(id, ":"); idx >= 0 && idx+1 < len(id) {
		id = id[idx+1:]
	}
	if id == "" {
		return ""
	}
	return id
}
