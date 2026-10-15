package client

// Per-user chat state kept outside the conversations themselves.
//
// Pinned chats are not a conversation property: Teams keeps them in the
// user property "favorites", a JSON object (sent as a string) mapping
// conversation ID to its position in the pinned list, e.g.
// {"48:notes":1,"19:…@unq.gbl.spaces":2}.  It is read with
// GET /v1/users/ME/properties and written whole with
// PUT /v1/users/ME/properties?name=favorites {"favorites":"<json>"}.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const favoritesProperty = "favorites"

// UserPropertiesError is a failed read or write of the user properties.
type UserPropertiesError struct {
	Status      int
	BodySnippet string
}

func (e UserPropertiesError) Error() string {
	return fmt.Sprintf("user properties request failed with status %d", e.Status)
}

// GetPinnedChats returns the user's pinned chats as conversation ID ->
// position (1 is the top of the list).
func (c *Client) GetPinnedChats(ctx context.Context) (map[string]int, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	var props map[string]json.RawMessage
	if err := c.userPropertiesJSON(ctx, http.MethodGet, c.userPropertiesURL(""), nil, &props); err != nil {
		return nil, err
	}
	return parseFavorites(props[favoritesProperty])
}

// SetChatPinned pins or unpins a chat.  The property holds the whole list,
// so this reads it, changes the one entry and writes it back; an unchanged
// list is not written.  A new pin goes to the bottom of the list.
func (c *Client) SetChatPinned(ctx context.Context, conversationID string, pinned bool) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("missing conversation id")
	}
	favorites, err := c.GetPinnedChats(ctx)
	if err != nil {
		return err
	}
	if _, isPinned := favorites[conversationID]; isPinned == pinned {
		return nil
	}
	if pinned {
		last := 0
		for _, pos := range favorites {
			last = max(last, pos)
		}
		favorites[conversationID] = last + 1
	} else {
		delete(favorites, conversationID)
	}
	encoded, err := json.Marshal(favorites)
	if err != nil {
		return err
	}
	body := map[string]string{favoritesProperty: string(encoded)}
	return c.userPropertiesJSON(ctx, http.MethodPut, c.userPropertiesURL(favoritesProperty), body, nil)
}

// parseFavorites decodes the favorites property, which the service returns
// as a string holding JSON.  A missing or empty property means no pins.
func parseFavorites(raw json.RawMessage) (map[string]int, error) {
	out := make(map[string]int)
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		// Tolerate the object form too, should the service ever send it.
		if objErr := json.Unmarshal(raw, &out); objErr != nil {
			return nil, fmt.Errorf("decode favorites: %w", err)
		}
		return out, nil
	}
	if strings.TrimSpace(encoded) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		return nil, fmt.Errorf("decode favorites: %w", err)
	}
	return out, nil
}

func (c *Client) userPropertiesURL(name string) string {
	baseURL := c.MessagesURL
	if baseURL == "" {
		baseURL = defaultMessagesURL
	}
	endpoint := deriveBaseURL(baseURL) + "/v1/users/ME/properties"
	if name != "" {
		endpoint += "?name=" + name
	}
	return endpoint
}

func (c *Client) userPropertiesJSON(ctx context.Context, method string, endpoint string, in any, out any) error {
	var body io.Reader
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.debugRequest("teams user properties request", endpoint, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusTooManyRequests {
			return RetryableError{
				Status:     resp.StatusCode,
				RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			return RetryableError{Status: resp.StatusCode}
		}
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return UserPropertiesError{Status: resp.StatusCode, BodySnippet: string(snippet)}
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// conversationPropertyURL is the URL of one of the user's own properties of
// a conversation: PUT {chat service}/v1/users/ME/conversations/{id}/properties?name={name}.
func (c *Client) conversationPropertyURL(conversationID string, name string) string {
	baseURL := c.MessagesURL
	if baseURL == "" {
		baseURL = defaultMessagesURL
	}
	return fmt.Sprintf("%s/v1/users/ME/conversations/%s/properties?name=%s",
		deriveBaseURL(baseURL), url.PathEscape(conversationID), url.QueryEscape(name))
}

// setConversationProperty sets one of the user's own properties of a
// conversation, as the Teams web app's setConversationProperty does.
func (c *Client) setConversationProperty(ctx context.Context, conversationID string, name string, value string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	return c.userPropertiesJSON(ctx, http.MethodPut, c.conversationPropertyURL(conversationID, name),
		map[string]string{name: value}, nil)
}

// SetChatMuted mutes or unmutes a chat: its "alerts" property, "false"
// while muted.
func (c *Client) SetChatMuted(ctx context.Context, conversationID string, muted bool) error {
	return c.setConversationProperty(ctx, conversationID, "alerts", strconv.FormatBool(!muted))
}

// MarkChatUnread marks a chat unread: a consumptionHorizonBookmark whose
// time is after the read position (see model.ConversationProperties.MarkedUnread),
// "<read time>;<now>;<read message>" from the chat's consumption horizon.
func (c *Client) MarkChatUnread(ctx context.Context, conversationID string, horizon string, now time.Time) error {
	fields := strings.Split(strings.TrimSpace(horizon), ";")
	readTime, readMessage := "0", "0"
	if len(fields) == 3 {
		readTime, readMessage = fields[0], fields[2]
	}
	bookmark := fmt.Sprintf("%s;%d;%s", readTime, now.UnixMilli(), readMessage)
	return c.setConversationProperty(ctx, conversationID, "consumptionHorizonBookmark", bookmark)
}
