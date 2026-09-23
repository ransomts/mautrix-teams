package connector

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
)

const presencePollInterval = 60 * time.Second

// mapTeamsPresence converts a Teams availability string to a Matrix presence and status message.
func mapTeamsPresence(availability string) (event.Presence, string) {
	switch availability {
	case "Available":
		return event.PresenceOnline, ""
	case "Busy":
		return event.PresenceOnline, "Busy"
	case "DoNotDisturb":
		return event.PresenceUnavailable, "Do Not Disturb"
	case "Away":
		return event.PresenceUnavailable, "Away"
	case "BeRightBack":
		return event.PresenceUnavailable, "Be Right Back"
	case "Offline":
		return event.PresenceOffline, ""
	default:
		return event.PresenceOffline, ""
	}
}

// pollPresence fetches presence for known ghost users and pushes updates to Matrix.
func (c *TeamsClient) pollPresence(ctx context.Context) {
	if c == nil || c.Meta == nil || c.Login == nil || c.Main == nil || c.Main.Bridge == nil {
		return
	}
	if c.presenceForbidden.Load() {
		return
	}
	log := zerolog.Ctx(ctx)

	if err := c.ensureValidGraphToken(ctx); err != nil {
		log.Debug().Err(err).Msg("Skipping presence poll: no valid graph token")
		return
	}
	graphToken, err := c.graphAccessToken()
	if err != nil || graphToken == "" {
		return
	}

	// Seed from users we have already seen: enterprise conversations carry no
	// member list, so knownUsers would otherwise stay empty.
	c.seedKnownUsersFromProfiles(ctx)

	// Collect known user IDs.
	c.knownUsersMu.Lock()
	if len(c.knownUsers) == 0 {
		c.knownUsersMu.Unlock()
		return
	}
	userIDs := make([]string, 0, len(c.knownUsers))
	for uid := range c.knownUsers {
		userIDs = append(userIDs, uid)
	}
	c.knownUsersMu.Unlock()

	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return
	}
	gc := graph.NewClient(httpClient)
	gc.AccessToken = graphToken

	// Batch limit is 650 per call.
	const batchLimit = 650
	for i := 0; i < len(userIDs); i += batchLimit {
		end := i + batchLimit
		if end > len(userIDs) {
			end = len(userIDs)
		}
		batch := userIDs[i:end]

		presenceMap, err := gc.GetBatchPresence(ctx, batch)
		if err != nil {
			if errors.Is(err, graph.ErrPresenceForbidden) {
				if c.presenceForbidden.CompareAndSwap(false, true) {
					log.Info().Msg("Presence polling disabled: tenant has not consented Presence.Read.All for this client")
				}
				return
			}
			log.Debug().Err(err).Msg("Presence batch request failed")
			continue
		}

		c.presenceMu.Lock()
		if c.presenceCache == nil {
			c.presenceCache = make(map[string]string)
		}
		for uid, p := range presenceMap {
			if p == nil {
				continue
			}
			prev, hasPrev := c.presenceCache[uid]
			if hasPrev && prev == p.Availability {
				continue
			}
			c.presenceCache[uid] = p.Availability

			matrixPresence, statusMsg := mapTeamsPresence(p.Availability)
			ghost, err := c.Main.Bridge.GetGhostByID(ctx, teamsUserIDToNetworkUserID(uid))
			if err != nil || ghost == nil {
				continue
			}
			// bridgev2.MatrixAPI doesn't expose SetPresence, so we type-assert
			// to the concrete matrix.ASIntent to access the raw mautrix client.
			if asIntent, ok := ghost.Intent.(*matrix.ASIntent); ok {
				_ = asIntent.Matrix.SetPresence(ctx, mautrix.ReqPresence{
					Presence:  matrixPresence,
					StatusMsg: statusMsg,
				})
			}
		}
		c.presenceMu.Unlock()
	}
}

// seedKnownUsersFromProfiles loads every profile's Teams user ID into the
// known-user set. Cheap and idempotent (trackKnownUser dedupes).
func (c *TeamsClient) seedKnownUsersFromProfiles(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil {
		return
	}
	ids, err := c.Main.DB.Profile.ListTeamsUserIDs(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to seed known users from profiles")
		return
	}
	for _, id := range ids {
		c.trackKnownUser(id)
	}
}

// trackKnownUser records a Teams user ID for presence polling.
func (c *TeamsClient) trackKnownUser(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.knownUsersMu.Lock()
	if c.knownUsers == nil {
		c.knownUsers = make(map[string]struct{})
	}
	c.knownUsers[userID] = struct{}{}
	c.knownUsersMu.Unlock()
}
