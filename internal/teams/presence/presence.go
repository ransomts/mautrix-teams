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
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Scope is what to ask the token endpoint for.
const Scope = "https://presence.teams.microsoft.com/.default"

const defaultBaseURL = "https://presence.teams.microsoft.com/"

// Paths on the presence service, as the Teams web app calls them.
const (
	getPresencePath       = "v1/presence/getpresence/"
	forceAvailabilityPath = "v1/me/forceavailability/"
	publishNotePath       = "v1/me/publishnote"
)

// Availabilities the user can set (SetAvailability); Offline is sent
// with the activity OffWork, as Teams does ("Appear offline").
var Availabilities = []string{"Available", "Busy", "DoNotDisturb", "BeRightBack", "Away", "Offline"}

// ErrUnauthorized is returned when the service rejects the token (401/403).
var ErrUnauthorized = errors.New("presence service rejected the token")

// Presence is one user's state.  Availability is Teams' coarse state
// (Available, Busy, DoNotDisturb, Away, BeRightBack, Offline, and the
// *Idle variants); Activity says why (InACall, InAMeeting, Presenting, …).
// Note is the status note the user set; CalendarData carries their
// out-of-office state and message.
type Presence struct {
	Availability string       `json:"availability"`
	Activity     string       `json:"activity"`
	Note         Note         `json:"note"`
	CalendarData CalendarData `json:"calendarData"`
}

// Note is a status or out-of-office note.  Message is HTML-escaped and may
// end in <pinnednote></pinnednote> ("show when people message me").
type Note struct {
	Message string `json:"message"`
	Expiry  string `json:"expiry"`
}

// CalendarData is the out-of-office part of a presence.
type CalendarData struct {
	IsOutOfOffice   bool `json:"isOutOfOffice"`
	OutOfOfficeNote Note `json:"outOfOfficeNote"`
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

// Text is the note as plain text, or "" when it is empty or expired at now.
// The Teams client hides expired notes the same way.
func (n Note) Text(now time.Time) string {
	if n.Expiry != "" {
		if exp, err := time.Parse(time.RFC3339, n.Expiry); err == nil && !exp.After(now) {
			return ""
		}
	}
	text := html.UnescapeString(tagPattern.ReplaceAllString(n.Message, " "))
	return strings.Join(strings.Fields(text), " ")
}

// Client queries the presence service.
type Client struct {
	HTTP  *http.Client
	Token string
	// BaseURL overrides the service's base URL (ending in "/"), for tests.
	BaseURL string
}

func (c *Client) url(path string) string {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return base + path
}

// do sends a request with the service's headers and returns the response
// body of a 2xx response.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c == nil || c.HTTP == nil {
		return nil, errors.New("missing presence HTTP client")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("missing presence token")
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-ms-client-type", "web")
	req.Header.Set("x-ms-client-user-agent", "Teams-V2-Web")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := data
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}
		return nil, fmt.Errorf("presence %s %s failed: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return data, nil
}

// SetAvailability forces the user's own availability (one of
// Availabilities) until expiry, or until changed when expiry is zero.
// An empty availability resets it to automatic.
func (c *Client) SetAvailability(ctx context.Context, availability string, expiry time.Time) error {
	if availability == "" {
		_, err := c.do(ctx, http.MethodPut, forceAvailabilityPath, nil)
		return err
	}
	body := map[string]string{"availability": availability}
	if availability == "Offline" {
		body["activity"] = "OffWork"
	}
	if !expiry.IsZero() {
		body["desiredExpirationTime"] = expiry.UTC().Format(time.RFC3339)
	}
	_, err := c.do(ctx, http.MethodPut, forceAvailabilityPath, body)
	return err
}

// PublishNote sets the user's own status note, plain text, until expiry
// (or until changed, when zero).  An empty message clears it.  With
// pinned, Teams shows the note to people when they message the user.
func (c *Client) PublishNote(ctx context.Context, message string, expiry time.Time, pinned bool) error {
	body := map[string]string{"message": ""}
	if message != "" {
		body["message"] = html.EscapeString(message)
		if pinned {
			body["message"] += "<pinnednote></pinnednote>"
		}
		if !expiry.IsZero() {
			body["expiry"] = expiry.UTC().Format(time.RFC3339)
		}
	}
	_, err := c.do(ctx, http.MethodPut, publishNotePath, body)
	return err
}

// Get returns the presence of each MRI ("8:orgid:<object id>") the service
// knows, keyed by MRI.
func (c *Client) Get(ctx context.Context, mris []string) (map[string]Presence, error) {
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
	data, err := c.do(ctx, http.MethodPost, getPresencePath, queries)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		MRI      string   `json:"mri"`
		Presence Presence `json:"presence"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
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
