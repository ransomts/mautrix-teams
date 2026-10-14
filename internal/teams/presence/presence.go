// Package presence reads users' availability from the Teams presence
// service, the one the Teams web app uses.  Graph's presence API needs
// Presence.Read.All, which a tenant may not have consented for the Teams
// web client; this service takes a token for its own resource instead
// (Scope), which that client can get.
package presence

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
const Scope = "https://presence.teams.microsoft.com/.default"

const defaultURL = "https://presence.teams.microsoft.com/v1/presence/getpresence/"

// ErrUnauthorized is returned when the service rejects the token (401/403).
var ErrUnauthorized = errors.New("presence service rejected the token")

// Presence is one user's state.  Availability is Teams' coarse state
// (Available, Busy, DoNotDisturb, Away, BeRightBack, Offline, and the
// *Idle variants); Activity says why (InACall, InAMeeting, Presenting, …).
type Presence struct {
	Availability string `json:"availability"`
	Activity     string `json:"activity"`
}

// Client queries the presence service.
type Client struct {
	HTTP  *http.Client
	Token string
	// URL overrides the endpoint, for tests.
	URL string
}

// Get returns the presence of each MRI ("8:orgid:<object id>") the service
// knows, keyed by MRI.
func (c *Client) Get(ctx context.Context, mris []string) (map[string]Presence, error) {
	if c == nil || c.HTTP == nil {
		return nil, errors.New("missing presence HTTP client")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("missing presence token")
	}
	if len(mris) == 0 {
		return nil, nil
	}
	type query struct {
		MRI string `json:"mri"`
	}
	queries := make([]query, len(mris))
	for i, mri := range mris {
		queries[i] = query{MRI: mri}
	}
	payload, err := json.Marshal(queries)
	if err != nil {
		return nil, err
	}
	endpoint := c.URL
	if endpoint == "" {
		endpoint = defaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-ms-client-type", "web")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("presence request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var entries []struct {
		MRI      string   `json:"mri"`
		Presence Presence `json:"presence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode presence response: %w", err)
	}
	out := make(map[string]Presence, len(entries))
	for _, e := range entries {
		if e.MRI != "" {
			out[e.MRI] = e.Presence
		}
	}
	return out, nil
}
