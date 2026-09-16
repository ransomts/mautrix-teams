package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth 2.0 device authorization grant (RFC 8628) against the Microsoft
// identity platform. Unlike tokens lifted from the Teams web app (a
// single-page app whose refresh tokens die after 24 hours), a public client
// using this grant receives a refresh token with the tenant's normal sliding
// lifetime (90 days by default).

const (
	deviceCodeGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	deviceCodeDefaultPoll   = 5 * time.Second
	deviceCodeSlowDownStep  = 5 * time.Second
	deviceCodeDefaultExpiry = 15 * time.Minute
)

// DeviceCode is the identity provider's response to a device authorization
// request.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Message         string
	ExpiresAt       time.Time
	Interval        time.Duration
}

// DeviceCodeError is a terminal error from the device code polling loop, as
// reported by the identity provider (expired_token, authorization_declined,
// bad_verification_code, invalid_grant).
type DeviceCodeError struct {
	Code        string
	Description string
}

func (e *DeviceCodeError) Error() string {
	if e.Description == "" {
		return "device code login failed: " + e.Code
	}
	return fmt.Sprintf("device code login failed: %s: %s", e.Code, e.Description)
}

// ErrDeviceCodeExpired is returned when the user did not complete the login
// before the code expired, either by the provider's error or our own clock.
var ErrDeviceCodeExpired = errors.New("device code expired before the login was completed")

// DeviceCodeEndpointFor derives the device authorization endpoint from a
// v2.0 token endpoint: ".../oauth2/v2.0/token" becomes ".../oauth2/v2.0/devicecode".
func DeviceCodeEndpointFor(tokenEndpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(tokenEndpoint), "/")
	if trimmed == "" {
		return ""
	}
	if strings.HasSuffix(trimmed, "/token") {
		return strings.TrimSuffix(trimmed, "/token") + "/devicecode"
	}
	return trimmed + "/devicecode"
}

// RequestDeviceCode starts a device authorization for the given scopes. The
// endpoint is derived from c.TokenEndpoint.
func (c *Client) RequestDeviceCode(ctx context.Context, scopes []string) (*DeviceCode, error) {
	if c == nil || c.HTTP == nil {
		return nil, errors.New("auth client missing HTTP client")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return nil, errors.New("missing client id for device code login")
	}
	endpoint := DeviceCodeEndpointFor(c.TokenEndpoint)
	if endpoint == "" {
		return nil, errors.New("missing token endpoint for device code login")
	}
	if len(scopes) == 0 {
		scopes = c.Scopes
	}

	values := url.Values{}
	values.Set("client_id", c.ClientID)
	values.Set("scope", strings.Join(scopes, " "))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(readBodySnippet(resp.Body, 400))
		if c.Log != nil {
			c.Log.Error().Int("status", resp.StatusCode).Str("body", snippet).Msg("Device code endpoint error")
		}
		if snippet == "" {
			return nil, fmt.Errorf("device code endpoint returned non-2xx status: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("device code endpoint returned non-2xx status: %d body=%s", resp.StatusCode, snippet)
	}

	var payload struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
		Message         string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.DeviceCode == "" || payload.UserCode == "" {
		return nil, errors.New("device code response missing device_code or user_code")
	}

	dc := &DeviceCode{
		DeviceCode:      payload.DeviceCode,
		UserCode:        payload.UserCode,
		VerificationURI: payload.VerificationURI,
		Message:         payload.Message,
		Interval:        deviceCodeDefaultPoll,
		ExpiresAt:       time.Now().Add(deviceCodeDefaultExpiry),
	}
	if payload.Interval > 0 {
		dc.Interval = time.Duration(payload.Interval) * time.Second
	}
	if payload.ExpiresIn > 0 {
		dc.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	if c.Log != nil {
		c.Log.Info().Time("expires_at", dc.ExpiresAt).Dur("interval", dc.Interval).Msg("Device code issued")
	}
	return dc, nil
}

// PollDeviceCode polls the token endpoint until the user completes the login,
// the code expires, the provider rejects it, or ctx is cancelled.
func (c *Client) PollDeviceCode(ctx context.Context, dc *DeviceCode) (*AuthState, error) {
	if c == nil || c.HTTP == nil {
		return nil, errors.New("auth client missing HTTP client")
	}
	if dc == nil || dc.DeviceCode == "" {
		return nil, errors.New("missing device code")
	}
	interval := dc.Interval
	if interval <= 0 {
		interval = deviceCodeDefaultPoll
	}
	scope := strings.Join(c.Scopes, " ")

	for {
		if !dc.ExpiresAt.IsZero() && time.Now().After(dc.ExpiresAt) {
			return nil, ErrDeviceCodeExpired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		state, pending, err := c.tryDeviceCodeToken(ctx, dc, scope)
		if err != nil {
			return nil, err
		}
		if pending == "" {
			return state, nil
		}
		if pending == "slow_down" {
			interval += deviceCodeSlowDownStep
		}
	}
}

// tryDeviceCodeToken makes one token request. It returns the state on
// success, or a non-empty pending code ("authorization_pending"/"slow_down")
// when the user has not finished yet.
func (c *Client) tryDeviceCodeToken(ctx context.Context, dc *DeviceCode, scope string) (*AuthState, string, error) {
	values := url.Values{}
	values.Set("grant_type", deviceCodeGrantType)
	values.Set("client_id", c.ClientID)
	values.Set("device_code", dc.DeviceCode)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		state, err := parseTokenResponse(resp.Body, scope)
		if err != nil {
			return nil, "", err
		}
		return state, "", nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var errPayload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &errPayload)
	switch errPayload.Error {
	case "authorization_pending", "slow_down":
		return nil, errPayload.Error, nil
	case "expired_token":
		return nil, "", ErrDeviceCodeExpired
	case "authorization_declined", "bad_verification_code", "invalid_grant":
		return nil, "", &DeviceCodeError{Code: errPayload.Error, Description: firstLine(errPayload.ErrorDescription)}
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 400 {
		snippet = snippet[:400] + "...(truncated)"
	}
	if errPayload.Error != "" {
		return nil, "", &DeviceCodeError{Code: errPayload.Error, Description: firstLine(errPayload.ErrorDescription)}
	}
	return nil, "", fmt.Errorf("token endpoint returned non-2xx status: %d body=%s", resp.StatusCode, snippet)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	return s
}
