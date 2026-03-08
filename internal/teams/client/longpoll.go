package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultEndpointsURL = "https://teams.live.com/api/chatsvc/consumer/v1/users/ME/endpoints"

// PollEvent represents a single notification event from the long-poll endpoint.
type PollEvent struct {
	ResourceType string `json:"resourceType"` // "NewMessage", "MessageUpdate", "EndpointPresence", "ThreadUpdate"
	Resource     string `json:"resource"`      // Resource path, e.g. "/v1/users/ME/conversations/19:xxx@thread.v2/messages/1234"
	ResourceLink string `json:"resourceLink"`  // Full URL to the resource
	Time         string `json:"time"`          // ISO timestamp
}

// RegisterEndpoint registers a notification endpoint for the current user.
// Returns the endpoint ID needed for long-polling.
//
// For enterprise accounts, the standard consumer-style endpoint registration
// may return 404 or 410. In that case, this method automatically retries with
// the enterprise chatsvcagg endpoint format.
func (c *Client) RegisterEndpoint(ctx context.Context) (string, error) {
	if c == nil || c.HTTP == nil {
		return "", ErrMissingHTTPClient
	}
	if c.Token == "" {
		return "", ErrMissingToken
	}

	endpointsURL := c.deriveEndpointsURL()

	if c.Log != nil {
		c.Log.Debug().
			Str("endpoints_url", endpointsURL).
			Str("send_messages_url", c.SendMessagesURL).
			Msg("Registering long-poll endpoint")
	}

	id, err := c.tryRegisterEndpoint(ctx, endpointsURL)
	if err == nil {
		return id, nil
	}

	// If the primary endpoint failed with 404 or 410, try an alternate URL.
	// Enterprise region endpoints may not support HTTP long-poll; fall back
	// to the consumer endpoint which sometimes works cross-tenant.
	var regErr *EndpointRegistrationError
	if errors.As(err, &regErr) && (regErr.Status == 404 || regErr.Status == 410) {
		altURL := c.deriveAlternateEndpointsURL()
		if altURL != "" && altURL != endpointsURL {
			if c.Log != nil {
				c.Log.Debug().
					Str("alt_endpoints_url", altURL).
					Int("primary_status", regErr.Status).
					Msg("Primary endpoint registration failed, trying enterprise format")
			}
			altID, altErr := c.tryRegisterEndpoint(ctx, altURL)
			if altErr == nil {
				return altID, nil
			}
		}
	}

	return "", err
}

// EndpointRegistrationError is returned when endpoint registration fails with
// a non-2xx HTTP status, allowing callers to inspect the status code.
type EndpointRegistrationError struct {
	Status      int
	BodySnippet string
}

func (e *EndpointRegistrationError) Error() string {
	return fmt.Sprintf("endpoint registration failed with status %d: %s", e.Status, e.BodySnippet)
}

func (c *Client) tryRegisterEndpoint(ctx context.Context, endpointsURL string) (string, error) {
	payload := map[string]interface{}{
		"endpointFeatures": "Agent,Presence2023",
		"subscriptions": []map[string]interface{}{
			{
				"channelType": "HttpLongPoll",
				"interestedResources": []string{
					"/v1/users/ME/conversations/ALL/messages",
					"/v1/users/ME/conversations/ALL/properties",
					"/v1/threads/ALL",
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointsURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return "", &EndpointRegistrationError{Status: resp.StatusCode, BodySnippet: string(snippet)}
	}

	// The endpoint ID is returned in the response body or Location header.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err == nil && result.ID != "" {
		return result.ID, nil
	}

	// Fallback: extract from Location header.
	location := resp.Header.Get("Location")
	if location != "" {
		parts := strings.Split(strings.TrimRight(location, "/"), "/")
		if len(parts) > 0 {
			return parts[len(parts)-1], nil
		}
	}

	return "", errors.New("could not extract endpoint ID from registration response")
}

// deriveEndpointsURL constructs the primary endpoint registration URL from SendMessagesURL.
func (c *Client) deriveEndpointsURL() string {
	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	if idx := strings.Index(baseURL, "/v1/users/ME"); idx != -1 {
		return baseURL[:idx] + "/v1/users/ME/endpoints"
	}
	return defaultEndpointsURL
}

// deriveAlternateEndpointsURL returns a fallback endpoint URL to try when the
// primary one fails. For enterprise accounts (region-specific URLs), this tries
// the consumer endpoint as a fallback since some enterprise setups route
// notifications through the consumer infrastructure.
func (c *Client) deriveAlternateEndpointsURL() string {
	baseURL := c.SendMessagesURL
	if baseURL == "" {
		return ""
	}
	// If primary was enterprise, try consumer as fallback.
	if !strings.Contains(baseURL, "teams.live.com") {
		return defaultEndpointsURL
	}
	return ""
}

// LongPoll performs a long-poll request for notification events.
// The timeout parameter controls how long the server will hold the connection.
// Returns events received during the poll period, or an empty slice on timeout.
func (c *Client) LongPoll(ctx context.Context, endpointID string, timeout time.Duration) ([]PollEvent, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return nil, errors.New("missing endpoint id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	base := deriveBaseURL(baseURL)
	pollURL := fmt.Sprintf("%s/v1/users/ME/endpoints/%s/subscriptions/0/poll", base, endpointID)

	timeoutSec := int(timeout.Seconds())
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	pollURL += fmt.Sprintf("?timeout=%d", timeoutSec)

	// Use a longer HTTP timeout than the poll timeout to avoid premature cancellation.
	httpCtx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("long poll failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		EventMessages []PollEvent `json:"eventMessages"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result.EventMessages, nil
}

// ExtractThreadIDFromResource parses a thread ID from a poll event resource path.
// Resource paths look like "/v1/users/ME/conversations/19:xxx@thread.v2/messages/1234"
func ExtractThreadIDFromResource(resource string) string {
	parts := strings.Split(resource, "/conversations/")
	if len(parts) < 2 {
		return ""
	}
	rest := parts[1]
	if idx := strings.Index(rest, "/"); idx > 0 {
		return rest[:idx]
	}
	return rest
}
