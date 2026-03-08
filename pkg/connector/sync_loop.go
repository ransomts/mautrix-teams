package connector

// Sync loop lifecycle and token refresh.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

const (
	threadDiscoveryInterval = 30 * time.Second
	selfMessageTTL          = 5 * time.Minute
)

func (c *TeamsClient) startSyncLoop() {
	if c == nil || c.Login == nil {
		return
	}
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	if c.syncCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(c.Login.Log.WithContext(context.Background()))
	c.syncCancel = cancel
	c.syncDone = make(chan struct{})
	go func() {
		defer close(c.syncDone)
		c.syncLoop(ctx)
	}()
}

func (c *TeamsClient) stopSyncLoop(timeout time.Duration) {
	c.syncMu.Lock()
	cancel := c.syncCancel
	done := c.syncDone
	c.syncCancel = nil
	c.syncDone = nil
	c.syncMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	if timeout <= 0 {
		<-done
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (c *TeamsClient) syncLoop(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	// Run once immediately to seed portals.
	err := c.syncOnce(ctx)
	if err != nil {
		log.Err(err).Msg("Teams sync loop initial run failed")
	}

	// Start presence polling in a separate goroutine.
	go c.presenceLoop(ctx)

	// Choose sync mode based on config.
	syncMode := "poll"
	if c.Main != nil && strings.EqualFold(c.Main.Config.SyncMode, "longpoll") {
		syncMode = "longpoll"
	}

	if syncMode == "longpoll" {
		log.Info().Msg("Starting Teams long-poll sync mode")
		if lpErr := c.longPollLoop(ctx, err == nil); lpErr != nil && !errors.Is(lpErr, context.Canceled) {
			log.Warn().Err(lpErr).Msg("Long-poll loop failed, falling back to short-polling")
			// Fall back to regular polling.
			if err := c.pollDueThreads(ctx, true); err != nil && !errors.Is(err, context.Canceled) {
				log.Err(err).Msg("Teams polling loop exited")
			}
		}
	} else {
		// Default: short-polling with adaptive backoff.
		if err := c.pollDueThreads(ctx, err == nil); err != nil && !errors.Is(err, context.Canceled) {
			log.Err(err).Msg("Teams polling loop exited")
		}
	}
}

// longPollLoop uses the Teams Consumer API long-polling endpoint for lower latency
// event delivery. It registers a notification endpoint, then enters a long-poll loop.
// When events arrive, it polls the affected threads for new messages.
func (c *TeamsClient) longPollLoop(ctx context.Context, initialDiscoverySucceeded bool) error {
	log := zerolog.Ctx(ctx)

	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	consumer := c.newConsumer()
	endpointID, err := consumer.RegisterEndpoint(ctx)
	if err != nil {
		return err
	}
	log.Info().Str("endpoint_id", endpointID).Msg("Registered Teams long-poll endpoint")

	const pollTimeout = 30 * time.Second
	nextDiscovery := time.Now().UTC().Add(threadDiscoveryInterval)
	if !initialDiscoverySucceeded {
		nextDiscovery = time.Time{}
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Periodic thread discovery refresh.
		now := time.Now().UTC()
		if nextDiscovery.IsZero() || !now.Before(nextDiscovery) {
			if err := c.refreshThreads(ctx); err != nil {
				log.Err(err).Msg("Teams thread discovery refresh failed (longpoll mode)")
			}
			nextDiscovery = now.Add(threadDiscoveryInterval)
		}

		if err := c.ensureValidSkypeToken(ctx); err != nil {
			return err
		}
		consumer.Token = c.Meta.SkypeToken

		events, err := consumer.LongPoll(ctx, endpointID, pollTimeout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			log.Warn().Err(err).Msg("Long-poll request failed, will retry")
			time.Sleep(2 * time.Second)
			continue
		}

		if len(events) == 0 {
			continue
		}

		// Collect unique thread IDs from events and poll them.
		affectedThreads := make(map[string]bool)
		for _, evt := range events {
			threadID := consumerclient.ExtractThreadIDFromResource(evt.Resource)
			if threadID != "" {
				affectedThreads[threadID] = true
			}
		}

		threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
		if err != nil {
			continue
		}
		for _, th := range threads {
			if affectedThreads[th.ThreadID] || affectedThreads[th.Conversation] {
				_, _ = c.pollThread(ctx, th, time.Now().UTC())
			}
		}
	}
}

func (c *TeamsClient) presenceLoop(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	ticker := time.NewTicker(presencePollInterval)
	defer ticker.Stop()

	// Initial poll after a short delay to let thread discovery populate known users.
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
		c.pollPresence(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.Meta != nil {
				if graphToken, err := c.Meta.GetGraphAccessToken(); err == nil && graphToken != "" {
					c.pollPresence(ctx)
				}
			}
		}
	}
	_ = log // suppress unused warning if needed
}

func (c *TeamsClient) syncOnce(ctx context.Context) error {
	log := c.log()
	log.Info().Msg("Starting full sync cycle")
	if err := c.refreshThreads(ctx); err != nil {
		log.Warn().Err(err).Msg("Thread discovery failed during sync")
		return err
	}
	log.Debug().Msg("Thread discovery complete, polling all threads")
	if err := c.pollAllThreadsOnce(ctx); err != nil {
		return err
	}
	log.Debug().Msg("Resolving unnamed group chats")
	c.resolveUnnamedGroupChats(ctx)
	log.Debug().Msg("Syncing team spaces")
	c.syncTeamSpaces(ctx)
	log.Debug().Msg("Applying structured room names")
	c.applyStructuredRoomNames(ctx)
	log.Info().Msg("Full sync cycle complete")
	return nil
}

func (c *TeamsClient) ensureValidSkypeToken(ctx context.Context) error {
	if c == nil || c.Login == nil {
		return errors.New("missing client/login")
	}
	log := c.log()
	if c.Meta == nil {
		if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok {
			c.Meta = meta
		}
	}
	if c.Meta == nil {
		return errors.New("missing login metadata")
	}

	now := time.Now().UTC()
	if c.Meta.SkypeToken != "" && c.Meta.SkypeTokenExpiresAt != 0 {
		expiresAt := time.Unix(c.Meta.SkypeTokenExpiresAt, 0).UTC()
		if now.Add(auth.SkypeTokenExpirySkew).Before(expiresAt) {
			log.Trace().Time("expires_at", expiresAt).Msg("Skype token still valid")
			return nil
		}
		log.Info().Time("expired_at", expiresAt).Msg("Skype token expired, refreshing")
	} else {
		log.Info().Msg("No Skype token, acquiring")
	}
	refresh := strings.TrimSpace(c.Meta.RefreshToken)
	if refresh == "" {
		return errors.New("missing refresh token, re-login required")
	}

	authClient := newConfiguredAuthClientForLogin(c.Main, c.Meta)
	// Keep this refresh scoped for skypetoken bootstrap. Graph token persistence
	// is best-effort and should not affect skypetoken acquisition.
	// The MBI scope only works on the /common endpoint, not tenant-specific ones.
	authClient.Scopes = []string{mbiRefreshScope}
	authClient.TokenEndpoint = mbiTokenEndpoint

	state, err := authClient.RefreshAccessToken(ctx, refresh)
	if err != nil {
		return err
	}
	if strings.TrimSpace(state.RefreshToken) != "" {
		c.Meta.RefreshToken = strings.TrimSpace(state.RefreshToken)
	}

	skResult, err := authClient.AcquireSkypeToken(ctx, state.AccessToken)
	if err != nil {
		return err
	}

	c.Meta.AccessTokenExpiresAt = state.ExpiresAtUnix
	c.Meta.SkypeToken = skResult.Token
	c.Meta.SkypeTokenExpiresAt = skResult.ExpiresAt
	// Only overwrite Graph token fields if refresh response includes them. Do not
	// wipe a stored valid Graph token when MBI refresh omits Graph tokens.
	if strings.TrimSpace(state.GraphAccessToken) != "" && state.GraphExpiresAt != 0 {
		c.Meta.GraphAccessToken = strings.TrimSpace(state.GraphAccessToken)
		c.Meta.GraphExpiresAt = state.GraphExpiresAt
	}
	c.Meta.TeamsUserID = auth.NormalizeTeamsUserID(skResult.SkypeID)
	if skResult.ChatServiceURL != "" {
		c.Meta.RegionChatServiceURL = skResult.ChatServiceURL
	}
	if skResult.AmsURL != "" {
		c.Meta.RegionAmsURL = skResult.AmsURL
	}
	c.Login.RemoteName = c.Meta.TeamsUserID

	log.Info().
		Str("teams_user_id", c.Meta.TeamsUserID).
		Time("skype_expires_at", time.Unix(c.Meta.SkypeTokenExpiresAt, 0).UTC()).
		Msg("Skype token refreshed successfully")
	if err := c.Login.Save(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to persist refreshed login metadata")
	}
	return nil
}
