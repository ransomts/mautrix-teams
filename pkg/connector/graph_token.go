package connector

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (c *TeamsClient) ensureValidGraphToken(ctx context.Context) error {
	if c == nil || c.Meta == nil || c.Login == nil {
		return errors.New("missing client/login metadata")
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := time.Now().UTC()
	if c.Meta.GraphTokenValid(now) {
		return nil
	}
	if err := c.graphRefreshFail.blocked(now); err != nil {
		return err
	}
	refreshToken := strings.TrimSpace(c.Meta.RefreshToken)
	if refreshToken == "" {
		return errors.New("missing refresh token for graph token refresh")
	}

	authClient := newConfiguredAuthClientForLogin(c.Main, c.Meta)

	refreshed, err := refreshAccessTokenForGraphScopeWithMeta(ctx, authClient, refreshToken, c.Main, c.Meta)
	if err != nil {
		c.graphRefreshFail.record(now, err)
		return err
	}
	if refreshed == nil || strings.TrimSpace(refreshed.GraphAccessToken) == "" || refreshed.GraphExpiresAt == 0 {
		err := errors.New("graph token refresh succeeded but did not return graph access token")
		c.graphRefreshFail.record(now, err)
		return err
	}
	c.graphRefreshFail.reset()

	if rt := strings.TrimSpace(refreshed.RefreshToken); rt != "" {
		c.Meta.RefreshToken = rt
	}
	c.Meta.GraphAccessToken = strings.TrimSpace(refreshed.GraphAccessToken)
	c.Meta.GraphExpiresAt = refreshed.GraphExpiresAt

	return c.saveLogin(ctx)
}
