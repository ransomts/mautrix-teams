package connector

import (
	"context"
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
	log := zerolog.Ctx(ctx)

	if err := c.ensureValidGraphToken(ctx); err != nil {
		log.Debug().Err(err).Msg("Skipping presence poll: no valid graph token")
		return
	}
	graphToken, err := c.Meta.GetGraphAccessToken()
	if err != nil || graphToken == "" {
		return
	}

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
			log.Debug().Err(err).Msg("Presence batch request failed")
			continue
		}

		c.presenceMu.Lock()
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
