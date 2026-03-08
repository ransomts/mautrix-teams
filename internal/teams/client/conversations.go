package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

const (
	defaultConversationsURL = "https://teams.live.com/api/chatsvc/consumer/v1/users/ME/conversations"
	maxErrorBodyBytes       = 2048
)

var ErrMissingHTTPClient = errors.New("consumer client missing http client")

type ConversationsError struct {
	Status      int
	BodySnippet string
}

func (e ConversationsError) Error() string {
	return "conversations request failed"
}

type Client struct {
	HTTP                   *http.Client
	Executor               *TeamsRequestExecutor
	ConversationsURL       string
	MessagesURL            string
	SendMessagesURL        string
	ConsumptionHorizonsURL string
	Token                  string
	Log                    *zerolog.Logger
}

func NewClient(httpClient *http.Client) *Client {
	executor := &TeamsRequestExecutor{
		HTTP:        httpClient,
		Log:         zerolog.Nop(),
		MaxRetries:  4,
		BaseBackoff: 500 * time.Millisecond,
		MaxBackoff:  10 * time.Second,
	}
	return &Client{
		HTTP:                   httpClient,
		Executor:               executor,
		ConversationsURL:       defaultConversationsURL,
		SendMessagesURL:        defaultSendMessagesURL,
		ConsumptionHorizonsURL: defaultConsumptionHorizonsURL,
	}
}

func (c *Client) ListConversations(ctx context.Context, token string) ([]model.RemoteConversation, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	endpoint := c.ConversationsURL
	if endpoint == "" {
		endpoint = defaultConversationsURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authentication", "skypetoken="+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, ConversationsError{
			Status:      resp.StatusCode,
			BodySnippet: string(snippet),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Conversations []model.RemoteConversation `json:"conversations"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Conversations, nil
}

// UpdateConversationTopic sets the topic (display name) for a group chat thread.
func (c *Client) UpdateConversationTopic(ctx context.Context, threadID string, topic string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("missing thread id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	endpoint := fmt.Sprintf("%s/%s/properties?name=topic", baseURL, url.PathEscape(threadID))

	payload := map[string]string{
		"topic": topic,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("update conversation topic failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

// CreateConversation creates a new 1:1 or DM chat with the given participants.
// participantMRIs should include the self user as the first entry (with role "Admin").
// Returns the thread ID from the Location header.
func (c *Client) CreateConversation(ctx context.Context, participantMRIs []string) (string, error) {
	return c.createConversationWithTopic(ctx, "", participantMRIs)
}

// CreateGroupConversation creates a new group chat with a topic and multiple participants.
// participantMRIs should include the self user as the first entry (with role "Admin").
// Returns the thread ID from the Location header.
func (c *Client) CreateGroupConversation(ctx context.Context, topic string, participantMRIs []string) (string, error) {
	return c.createConversationWithTopic(ctx, topic, participantMRIs)
}

// AddMember adds a user to a group chat thread.
func (c *Client) AddMember(ctx context.Context, threadID string, memberMRI string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	memberMRI = strings.TrimSpace(memberMRI)
	if threadID == "" || memberMRI == "" {
		return errors.New("missing thread id or member MRI")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	threadsBase := deriveBaseURL(baseURL)
	endpoint := fmt.Sprintf("%s/v1/threads/%s/members/%s",
		threadsBase, url.PathEscape(threadID), url.PathEscape(memberMRI))

	payload := map[string]string{"role": "User"}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("add member failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

// RemoveMember removes a user from a group chat thread.
func (c *Client) RemoveMember(ctx context.Context, threadID string, memberMRI string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	memberMRI = strings.TrimSpace(memberMRI)
	if threadID == "" || memberMRI == "" {
		return errors.New("missing thread id or member MRI")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	threadsBase := deriveBaseURL(baseURL)
	endpoint := fmt.Sprintf("%s/v1/threads/%s/members/%s",
		threadsBase, url.PathEscape(threadID), url.PathEscape(memberMRI))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("remove member failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

func (c *Client) createConversationWithTopic(ctx context.Context, topic string, participantMRIs []string) (string, error) {
	if c == nil || c.HTTP == nil {
		return "", ErrMissingHTTPClient
	}
	if c.Token == "" {
		return "", ErrMissingToken
	}
	if len(participantMRIs) == 0 {
		return "", errors.New("at least one participant is required")
	}

	members := make([]map[string]string, 0, len(participantMRIs))
	for i, mri := range participantMRIs {
		role := "User"
		if i == 0 {
			role = "Admin"
		}
		members = append(members, map[string]string{
			"id":   mri,
			"role": role,
		})
	}

	properties := map[string]string{
		"threadType": "chat",
	}
	if strings.TrimSpace(topic) != "" {
		properties["topic"] = topic
	}

	payload := map[string]interface{}{
		"members":    members,
		"properties": properties,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	// The threads endpoint is at the base URL level, not under /users/ME/conversations.
	threadsURL := deriveBaseURL(baseURL) + "/v1/threads"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, threadsURL, bytes.NewReader(body))
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
		return "", fmt.Errorf("create conversation failed with status %d: %s", resp.StatusCode, string(snippet))
	}

	// Drain body before reading headers.
	_, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	// Thread ID is returned in the Location header.
	location := resp.Header.Get("Location")
	if location == "" {
		return "", errors.New("create conversation response missing Location header")
	}
	// Location is typically the full URL; extract the thread ID (last path segment).
	parts := strings.Split(strings.TrimRight(location, "/"), "/")
	threadID := parts[len(parts)-1]
	if threadID == "" {
		return "", errors.New("could not extract thread ID from Location header")
	}
	return threadID, nil
}

// deriveBaseURL extracts the service base URL (without API path) from a full
// API URL like "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations".
// This allows thread/member management URLs to work with enterprise region URLs.
func deriveBaseURL(fullURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(fullURL), "/")
	if idx := strings.Index(trimmed, "/v1/users/ME"); idx != -1 {
		return trimmed[:idx]
	}
	if idx := strings.Index(trimmed, "/v1/threads"); idx != -1 {
		return trimmed[:idx]
	}
	return strings.TrimRight(trimmed, "/")
}
