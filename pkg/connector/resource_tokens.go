package connector

import (
	"context"
	"errors"
	"strings"
	"time"
)

// resourceTokenEntry is an access token for one resource, kept in memory.
type resourceTokenEntry struct {
	token   string
	expiry  time.Time
	refresh refreshFailure
}

// resourceToken returns an access token for scope (a resource's
// "/.default"), refreshing it from the login's refresh token when it is
// missing or about to expire, and whether it was just issued.  Tokens live
// only in memory: one refresh an hour per resource is cheap.  The Teams web
// client's refresh token gets tokens for every resource the web app uses,
// such as the presence service and the middle tier.
func (c *TeamsClient) resourceToken(ctx context.Context, scope string) (string, bool, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.superseded() {
		return "", false, errClientSuperseded
	}
	if c.resourceTokens == nil {
		c.resourceTokens = make(map[string]*resourceTokenEntry)
	}
	entry := c.resourceTokens[scope]
	if entry == nil {
		entry = &resourceTokenEntry{}
		c.resourceTokens[scope] = entry
	}
	now := time.Now().UTC()
	if entry.token != "" && now.Add(5*time.Minute).Before(entry.expiry) {
		return entry.token, false, nil
	}
	if err := entry.refresh.blocked(now); err != nil {
		return "", false, err
	}
	refreshToken := strings.TrimSpace(c.Meta.RefreshToken)
	if refreshToken == "" {
		return "", false, errors.New("missing refresh token")
	}
	authClient := *newConfiguredAuthClientForLogin(c.Main, c.Meta)
	authClient.Scopes = []string{scope, "openid", "profile", "offline_access"}
	refreshed, err := authClient.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		entry.refresh.record(now, err)
		return "", false, err
	}
	entry.refresh.reset()
	entry.token = strings.TrimSpace(refreshed.AccessToken)
	entry.expiry = refreshed.Expiry()
	if rt := strings.TrimSpace(refreshed.RefreshToken); rt != "" && rt != c.Meta.RefreshToken {
		c.Meta.RefreshToken = rt
		if err := c.saveLogin(ctx); err != nil {
			log := c.log()
			log.Warn().Err(err).Msg("Failed to save refresh token from a resource token refresh")
		}
	}
	return entry.token, true, nil
}

// dropResourceToken forgets scope's token, so the next use gets a new one.
func (c *TeamsClient) dropResourceToken(scope string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if entry := c.resourceTokens[scope]; entry != nil {
		entry.token = ""
	}
}
