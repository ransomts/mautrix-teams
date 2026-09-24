package connector

// Sync loop lifecycle and token refresh.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

const (
	threadDiscoveryInterval = 30 * time.Second
	selfMessageTTL          = 5 * time.Minute
	// longPollRetryDelay is how long to wait after a failed long-poll request.
	longPollRetryDelay = 2 * time.Second
	// longPollMaxRetryDelay caps the backoff between failed long-poll requests.
	longPollMaxRetryDelay = 60 * time.Second
	// longPollReregisterAfter is how many failures in a row trigger a fresh
	// endpoint registration.
	longPollReregisterAfter = 3
	// presenceInitialDelay lets thread discovery populate known users before
	// the first presence poll.
	presenceInitialDelay = 10 * time.Second
)

func (c *TeamsClient) startSyncLoop() {
	c.startSyncTasks(c.syncLoop)
}

// syncTasks runs the sync loop's goroutines and knows when all of them have
// returned, so that stopping the loop can wait for the long-poll and
// presence loops too, not just the poll loop. Otherwise they could keep
// polling (and refreshing tokens) after Disconnect or next to the client a
// re-login starts.
type syncTasks struct {
	wg sync.WaitGroup
}

// Go runs f in a goroutine that stopping the sync loop waits for.
func (t *syncTasks) Go(f func()) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		f()
	}()
}

// startSyncTasks runs run as the root sync goroutine, unless one is running.
// run starts the others through tasks.
func (c *TeamsClient) startSyncTasks(run func(ctx context.Context, tasks *syncTasks)) {
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
	done := make(chan struct{})
	c.syncDone = done
	tasks := &syncTasks{}
	// run is counted before anything waits, and starts the others while it
	// is still counted, so the count cannot reach zero early.
	tasks.Go(func() { run(ctx, tasks) })
	go func() {
		tasks.wg.Wait()
		close(done)
	}()
}

// stopSyncLoop cancels the sync goroutines and waits up to timeout (for
// ever if timeout <= 0) for all of them to return.
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
		// A request stuck past its context; it ends when that request does.
		log := c.log()
		log.Warn().Dur("timeout", timeout).Msg("Sync goroutines still running after stop timeout")
	}
}

func (c *TeamsClient) syncLoop(ctx context.Context, tasks *syncTasks) {
	log := zerolog.Ctx(ctx)
	// Run once immediately to seed portals.
	err := c.syncOnce(ctx)
	if err != nil {
		log.Err(err).Msg("Teams sync loop initial run failed")
	}

	// Start presence polling in a separate goroutine.
	tasks.Go(func() { c.presenceLoop(ctx) })

	// Choose sync mode based on config.
	syncMode := "poll"
	if c.Main != nil && strings.EqualFold(c.Main.Config.SyncMode, "longpoll") {
		syncMode = "longpoll"
	}

	if syncMode == "longpoll" {
		// Long-poll notifications wake the poll loop for the threads they
		// name; the poll loop keeps running as a backstop, at a slower idle
		// cadence while notifications arrive, and alone if they stop.
		log.Info().Msg("Starting Teams long-poll sync mode")
		tasks.Go(func() {
			if lpErr := c.longPollLoop(ctx); lpErr != nil && !loopStopped(lpErr) {
				log.Warn().Err(lpErr).Msg("Long-poll loop failed, polling alone")
			}
			c.longPollUp.Store(false)
		})
		if err := c.pollDueThreads(ctx, err == nil); err != nil && !loopStopped(err) {
			log.Err(err).Msg("Teams polling loop exited")
		}
	} else {
		// Default: short-polling with adaptive backoff.
		if err := c.pollDueThreads(ctx, err == nil); err != nil && !loopStopped(err) {
			log.Err(err).Msg("Teams polling loop exited")
		}
	}
}

// loopStopped reports whether a loop ended because it was told to, not
// because it failed.
func loopStopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, errClientSuperseded)
}

// longPollLoop holds a Teams long-poll request open and asks the poll loop to
// poll every thread an event names. It runs until ctx ends or registration
// fails; a failing endpoint (expired, or lost with the network) is registered
// afresh after longPollReregisterAfter failures in a row.
func (c *TeamsClient) longPollLoop(ctx context.Context) error {
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
	failures := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.superseded() {
			return errClientSuperseded
		}
		if err := c.ensureValidSkypeToken(ctx); err != nil {
			return err
		}
		c.applyTokenToConsumer(consumer)

		events, err := consumer.LongPoll(ctx, endpointID, pollTimeout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			c.longPollUp.Store(false)
			failures++
			log.Warn().Err(err).Int("failures", failures).Msg("Long-poll request failed, will retry")
			if failures%longPollReregisterAfter == 0 {
				if id, regErr := consumer.RegisterEndpoint(ctx); regErr != nil {
					log.Warn().Err(regErr).Msg("Long-poll endpoint re-registration failed")
				} else {
					endpointID = id
					log.Info().Str("endpoint_id", endpointID).Msg("Re-registered Teams long-poll endpoint")
				}
			}
			delay := longPollRetryDelay << min(failures-1, 5)
			if delay > longPollMaxRetryDelay {
				delay = longPollMaxRetryDelay
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		failures = 0
		c.longPollUp.Store(true)

		for _, evt := range events {
			threadID := evt.ThreadID()
			log.Debug().Str("resource_type", evt.ResourceType).Str("thread_id", threadID).Msg("Long-poll event")
			if threadID == "" {
				continue
			}
			// Anything but a message may be a read-position change.
			receipts := evt.ResourceType != "NewMessage" && evt.ResourceType != "MessageUpdate"
			c.requestPoll(threadID, receipts, "longpoll")
		}
	}
}

func (c *TeamsClient) presenceLoop(ctx context.Context) {
	ticker := time.NewTicker(presencePollInterval)
	defer ticker.Stop()

	// Initial poll after a short delay to let thread discovery populate known users.
	select {
	case <-ctx.Done():
		return
	case <-time.After(presenceInitialDelay):
		c.pollPresence(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.superseded() {
				return
			}
			if c.Meta != nil {
				if graphToken, err := c.graphAccessToken(); err == nil && graphToken != "" {
					c.pollPresence(ctx)
				}
			}
		}
	}
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
	// Fetch the team/channel map once and reuse it for both space naming and
	// structured room names, so team spaces can be named even when
	// me/joinedTeams is inconsistent with the channels the user actually sees.
	channelMap := c.fetchTeamChannelMap(ctx)
	log.Debug().Msg("Repairing team spaces")
	c.repairTeamSpaces(ctx)
	log.Debug().Msg("Syncing team spaces")
	c.syncTeamSpaces(ctx, channelMap)
	log.Debug().Msg("Applying structured room names")
	c.applyStructuredRoomNames(ctx, channelMap)
	log.Debug().Msg("Syncing channel parents")
	c.syncChannelParents(ctx, channelMap)
	log.Info().Msg("Full sync cycle complete")
	return nil
}

// refreshFailure remembers a failed token refresh so that the many callers of
// ensureValidSkypeToken / ensureValidGraphToken (one per thread poll, per
// Matrix event, per presence tick) do not each re-hit the identity provider
// while the refresh token is known to be bad.
type refreshFailure struct {
	failures int
	until    time.Time
	err      error
}

const (
	refreshBackoffBase = 30 * time.Second
	refreshBackoffMax  = 15 * time.Minute
	// A refresh that got no answer is retried at least this often, so the
	// bridge picks up within a couple of minutes of the network coming back.
	refreshNetworkBackoffMax = 2 * time.Minute
)

// isPermanentRefreshError reports whether the identity provider rejected the
// refresh token itself (expired, revoked, consumed), in which case retrying
// cannot help until the user logs in again.
func isPermanentRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_grant") || strings.Contains(msg, "interaction_required")
}

func (f *refreshFailure) record(now time.Time, err error) {
	f.failures++
	delay := refreshBackoffBase
	if f.failures > 1 {
		delay = refreshBackoffBase * time.Duration(1<<uint(min(f.failures-1, 10)))
	}
	if delay > refreshBackoffMax || isPermanentRefreshError(err) {
		delay = refreshBackoffMax
	}
	if isTeamsNetworkError(err) && delay > refreshNetworkBackoffMax {
		delay = refreshNetworkBackoffMax
	}
	f.until = now.Add(delay)
	f.err = err
}

// blocked returns the cached error while the backoff window is open.
func (f *refreshFailure) blocked(now time.Time) error {
	if f.err == nil || !now.Before(f.until) {
		return nil
	}
	return fmt.Errorf("%w (refresh suppressed until %s)", f.err, f.until.UTC().Format(time.RFC3339))
}

func (f *refreshFailure) reset() {
	*f = refreshFailure{}
}

func (c *TeamsClient) ensureValidSkypeToken(ctx context.Context) error {
	if c == nil || c.Login == nil {
		return errors.New("missing client/login")
	}
	log := c.log()
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.Meta == nil {
		if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok {
			c.Meta = meta
		}
	}
	if c.Meta == nil {
		return errors.New("missing login metadata")
	}
	if c.superseded() {
		return errClientSuperseded
	}

	now := time.Now().UTC()
	if c.Meta.SkypeToken != "" && c.Meta.SkypeTokenExpiresAt != 0 {
		expiresAt := time.Unix(c.Meta.SkypeTokenExpiresAt, 0).UTC()
		if now.Add(auth.SkypeTokenExpirySkew).Before(expiresAt) {
			log.Trace().Time("expires_at", expiresAt).Msg("Skype token still valid")
			return nil
		}
		if err := c.skypeRefreshFail.blocked(now); err != nil {
			return err
		}
		log.Info().Time("expired_at", expiresAt).Msg("Skype token expired, refreshing")
	} else {
		if err := c.skypeRefreshFail.blocked(now); err != nil {
			return err
		}
		log.Info().Msg("No Skype token, acquiring")
	}
	refresh := strings.TrimSpace(c.Meta.RefreshToken)
	if refresh == "" {
		return errors.New("missing refresh token, re-login required")
	}

	authClient := newConfiguredAuthClientForLogin(c.Main, c.Meta)
	if scope := strings.TrimSpace(c.Meta.RefreshScope); scope != "" {
		// Explicit per-login scope (e.g. device code): refresh with it against
		// the configured (tenant) token endpoint.
		authClient.Scopes = strings.Fields(scope)
	} else if ep := tenantTokenEndpoint(c.Main); ep != "" {
		// Enterprise tenant: refresh the skypetoken with the delegated Spaces
		// scope against the tenant endpoint, the grant the browser uses. This
		// is driven by config (network.token_endpoint), so it does not depend
		// on per-login metadata surviving a save. The MBI scope on /common only
		// works for consumer accounts and enterprise tenants reject it.
		authClient.Scopes = strings.Fields(skypeSpacesRefreshScope)
		authClient.TokenEndpoint = ep
	} else {
		// Consumer account: MBI scope on the /common endpoint.
		authClient.Scopes = []string{mbiRefreshScope}
		authClient.TokenEndpoint = mbiTokenEndpointFor(authClient.TokenEndpoint)
	}

	state, err := authClient.RefreshAccessToken(ctx, refresh)
	if err != nil {
		c.skypeRefreshFail.record(now, err)
		if isPermanentRefreshError(err) {
			c.loggedIn.Store(false)
		}
		return err
	}
	refreshRotated := false
	if newRefresh := strings.TrimSpace(state.RefreshToken); newRefresh != "" {
		refreshRotated = newRefresh != refresh
		c.Meta.RefreshToken = newRefresh
	}

	skResult, err := authClient.AcquireSkypeToken(ctx, state.AccessToken)
	if err != nil {
		c.skypeRefreshFail.record(now, err)
		// The identity provider may already have retired the old refresh
		// token, so keep the new one even though this refresh failed.
		if refreshRotated {
			if saveErr := c.saveLogin(ctx); saveErr != nil {
				log.Error().Err(saveErr).Msg("Failed to persist rotated refresh token")
			}
		}
		return err
	}
	c.skypeRefreshFail.reset()
	c.clearBadCredentials()

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
	// bridgev2 reads RemoteName from its own goroutines without a lock, so
	// only write it when it actually changes (once per login, not on every
	// refresh).
	if name := c.remoteDisplayName(ctx, c.Meta.TeamsUserID); name != c.Login.RemoteName {
		c.Login.RemoteName = name
	}
	c.refreshCachedConsumerTokenLocked()

	log.Info().
		Str("teams_user_id", c.Meta.TeamsUserID).
		Time("skype_expires_at", time.Unix(c.Meta.SkypeTokenExpiresAt, 0).UTC()).
		Msg("Skype token refreshed successfully")
	if err := c.saveLogin(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to persist refreshed login metadata")
	}
	return nil
}
