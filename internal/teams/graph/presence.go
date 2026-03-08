package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UserPresence represents a user's presence status from the Graph API.
type UserPresence struct {
	Availability string `json:"availability"` // Available, Busy, DoNotDisturb, Away, Offline, etc.
	Activity     string `json:"activity"`     // Available, InACall, InAMeeting, Away, etc.
}

// GetUserPresence fetches a user's presence status from the Graph API.
// The userID should be the Azure AD object ID or a Teams MRI (prefix is stripped).
func (c *GraphClient) GetUserPresence(ctx context.Context, userID string) (*UserPresence, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}

	objectID := extractObjectID(userID)
	if objectID == "" {
		return nil, fmt.Errorf("cannot extract Azure AD object ID from %q", userID)
	}

	endpoint := "https://graph.microsoft.com/v1.0/users/" + objectID + "/presence"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, fmt.Errorf("graph presence request failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var presence UserPresence
	if err := json.Unmarshal(body, &presence); err != nil {
		return nil, err
	}
	return &presence, nil
}

// SetUserPresence sets the current user's preferred presence state.
// availability: Available, Busy, DoNotDisturb, Away, Offline
// activity: should generally match availability
// expirationDuration: ISO 8601 duration, e.g. "PT1H" for 1 hour
func (c *GraphClient) SetUserPresence(ctx context.Context, availability string, activity string, expirationDuration string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return ErrMissingGraphAccessToken
	}

	payload, err := json.Marshal(map[string]string{
		"availability":       availability,
		"activity":           activity,
		"expirationDuration": expirationDuration,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://graph.microsoft.com/v1.0/me/presence/setUserPreferredPresence",
		strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return fmt.Errorf("set presence failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

// GetBatchPresence fetches presence for multiple users at once.
func (c *GraphClient) GetBatchPresence(ctx context.Context, userIDs []string) (map[string]*UserPresence, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}
	if len(userIDs) == 0 {
		return nil, nil
	}

	// Extract object IDs.
	objectIDs := make([]string, 0, len(userIDs))
	idMapping := make(map[string]string, len(userIDs)) // objectID -> original
	for _, uid := range userIDs {
		objID := extractObjectID(uid)
		if objID != "" {
			objectIDs = append(objectIDs, objID)
			idMapping[objID] = uid
		}
	}
	if len(objectIDs) == 0 {
		return nil, nil
	}

	payload, err := json.Marshal(map[string]any{
		"ids": objectIDs,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://graph.microsoft.com/v1.0/communications/getPresencesByUserId",
		strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, fmt.Errorf("graph batch presence request failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Value []struct {
			ID           string `json:"id"`
			Availability string `json:"availability"`
			Activity     string `json:"activity"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	presenceMap := make(map[string]*UserPresence, len(result.Value))
	for _, p := range result.Value {
		originalID := idMapping[p.ID]
		if originalID == "" {
			originalID = p.ID
		}
		presenceMap[originalID] = &UserPresence{
			Availability: p.Availability,
			Activity:     p.Activity,
		}
	}
	return presenceMap, nil
}
