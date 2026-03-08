package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	skypeTokenErrorSnippetLimit = 2048
	SkypeTokenExpirySkew        = 60 * time.Second
)

type skypeTokenInner struct {
	SkypeToken string `json:"skypetoken"`
	ExpiresIn  int64  `json:"expiresIn"`
	SkypeID    string `json:"skypeid"`
	SignInName string `json:"signinname"`
	IsBusiness bool   `json:"isBusinessTenant"`
	// Enterprise endpoint uses "skypeToken" (capitalized) instead of "skypetoken"
	SkypeTokenAlt string `json:"skypeToken"`
	TokenType     string `json:"tokenType"`
}

type skypeTokenRegionGtms struct {
	ChatService    string `json:"chatService"`
	ChatServiceAfd string `json:"chatServiceAfd"`
	Ams            string `json:"ams"`
	AmsV2          string `json:"amsV2"`
}

type skypeTokenResponse struct {
	// Consumer endpoint nests under "skypeToken"
	SkypeToken skypeTokenInner `json:"skypeToken"`
	// Enterprise endpoint nests under "tokens"
	Tokens     skypeTokenInner    `json:"tokens"`
	RegionGtms skypeTokenRegionGtms `json:"regionGtms"`
}

// SkypeTokenResult holds the parsed result of a skype token acquisition.
type SkypeTokenResult struct {
	Token          string
	ExpiresAt      int64
	SkypeID        string
	ChatServiceURL string
	AmsURL         string
}

func (c *Client) AcquireSkypeToken(ctx context.Context, accessToken string) (*SkypeTokenResult, error) {
	if c.SkypeTokenEndpoint == "" {
		return nil, errors.New("skype token endpoint not configured")
	}
	if accessToken == "" {
		return nil, errors.New("missing access token for skypetoken acquisition")
	}
	if c.Log != nil {
		c.Log.Info().Msg("Acquiring Teams skypetoken")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.SkypeTokenEndpoint, strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet := strings.TrimSpace(readBodySnippet(resp.Body, skypeTokenErrorSnippetLimit))
		if len(snippet) > 400 {
			snippet = snippet[:400] + "...(truncated)"
		}
		if c.Log != nil {
			c.Log.Error().Int("status", resp.StatusCode).Str("body_snippet", snippet).Msg("Failed to acquire skypetoken")
		}
		if snippet == "" {
			return nil, fmt.Errorf("skypetoken endpoint returned non-2xx status: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("skypetoken endpoint returned non-2xx status: %d body=%s", resp.StatusCode, snippet)
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var payload skypeTokenResponse
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return nil, err
	}

	// Log the full regionGtms response for debugging enterprise endpoint URLs.
	if c.Log != nil {
		// Log regionGtms and any other top-level keys for endpoint discovery.
		var rawMap map[string]json.RawMessage
		if json.Unmarshal(rawBody, &rawMap) == nil {
			keys := make([]string, 0, len(rawMap))
			for k := range rawMap {
				keys = append(keys, k)
			}
			c.Log.Debug().
				Strs("response_keys", keys).
				Str("chatService", payload.RegionGtms.ChatService).
				Str("chatServiceAfd", payload.RegionGtms.ChatServiceAfd).
				Str("ams", payload.RegionGtms.Ams).
				Str("amsV2", payload.RegionGtms.AmsV2).
				Msg("Skypetoken regionGtms response")
		}
	}

	// Try consumer format first ("skypeToken" key), then enterprise ("tokens" key).
	inner := payload.SkypeToken
	if inner.SkypeToken == "" && inner.SkypeTokenAlt == "" {
		inner = payload.Tokens
	}

	token := inner.SkypeToken
	if token == "" {
		token = inner.SkypeTokenAlt
	}
	if token == "" {
		return nil, errors.New("skypetoken response missing token")
	}

	skypeID := inner.SkypeID
	if skypeID == "" {
		// Enterprise endpoints don't include skypeid in the response body.
		// Extract it from the JWT payload.
		skypeID = extractSkypeIDFromJWT(token)
	}

	var expiresAt int64
	if inner.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(inner.ExpiresIn) * time.Second).Unix()
	}
	return &SkypeTokenResult{
		Token:          token,
		ExpiresAt:      expiresAt,
		SkypeID:        skypeID,
		ChatServiceURL: strings.TrimSpace(payload.RegionGtms.ChatService),
		AmsURL:         strings.TrimSpace(payload.RegionGtms.Ams),
	}, nil
}

func (a *AuthState) HasValidSkypeToken(now time.Time) bool {
	if a == nil || a.SkypeToken == "" || a.SkypeTokenExpiresAt == 0 {
		return false
	}
	expiresAt := time.Unix(a.SkypeTokenExpiresAt, 0).UTC()
	return now.UTC().Add(SkypeTokenExpirySkew).Before(expiresAt)
}

func extractSkypeIDFromJWT(token string) string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		SkypeID string `json:"skypeid"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.SkypeID
}

func readBodySnippet(r io.Reader, limit int64) string {
	if r == nil || limit <= 0 {
		return ""
	}
	limited := io.LimitReader(r, limit)
	body, _ := io.ReadAll(limited)
	return string(body)
}
