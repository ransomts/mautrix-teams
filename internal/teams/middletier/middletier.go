// Package middletier calls the Teams middle tier ("mt"), the service the
// Teams web app uses for user settings such as automatic replies.  Its
// base URL is the tenant's regionGtms middleTier; requests carry a token
// for api.spaces.skype.com and, as the web app sends them, the skype token.
package middletier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Scope is what to ask the token endpoint for.
const Scope = "https://api.spaces.skype.com/.default"

const (
	automaticRepliesPath       = "beta/users/automaticRepliesSetting"
	updateAutomaticRepliesPath = "beta/users/updateAutomaticRepliesSetting"
)

// ErrUnauthorized is returned for a 401 or 403.
var ErrUnauthorized = errors.New("middle tier rejected the token")

// Client calls the middle tier.
type Client struct {
	HTTP       *http.Client
	BaseURL    string // regionGtms middleTier
	Token      string // for Scope
	SkypeToken string
}

func (c *Client) do(ctx context.Context, method, path string, in any, out any) error {
	if c == nil || c.HTTP == nil {
		return errors.New("missing middle tier HTTP client")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("middle tier URL not known yet")
	}
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+"/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	if c.SkypeToken != "" {
		req.Header.Set("x-skypetoken", c.SkypeToken)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w (%d): %s", ErrUnauthorized, resp.StatusCode, snippet(data))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("middle tier %s %s failed: %d %s", method, path, resp.StatusCode, snippet(data))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func snippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// GetAutomaticReplies returns the user's automatic-replies (out of office)
// setting as the middle tier gives it.
func (c *Client) GetAutomaticReplies(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, automaticRepliesPath, nil, &out)
	return out, err
}

// UpdateAutomaticReplies sets the user's automatic-replies setting.
func (c *Client) UpdateAutomaticReplies(ctx context.Context, setting map[string]any) error {
	return c.do(ctx, http.MethodPost, updateAutomaticRepliesPath, setting, nil)
}
