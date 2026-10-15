package connector

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/presence"
)

const presencePollInterval = 60 * time.Second

// presenceKeepAliveInterval is how often the presence of everyone not
// offline is sent again.  Synapse sets a user offline 30 seconds after
// their last sync or presence update (SYNC_ONLINE_TIMEOUT), and ghosts
// never sync, so a presence sent once would last half a minute.
const presenceKeepAliveInterval = 20 * time.Second

// presenceBatchSize is how many users one presence request asks about.
const presenceBatchSize = 100

// presenceStatus is the Matrix presence and status message for Teams
// presence p at now.  The message's first line is Teams' word for the
// state ("In a meeting"; empty for plain available or offline); a status
// note and an out-of-office message follow on lines of their own,
// "Note: …" and "Out of office: …", the only way to carry them, since a
// Matrix presence has no other text.
func presenceStatus(p presence.Presence, now time.Time) (event.Presence, string) {
	mp, status := mapTeamsPresence(p.Availability, p.Activity)
	lines := []string{status}
	if note := p.Note.Text(now); note != "" {
		lines = append(lines, "Note: "+note)
	}
	if oof := p.CalendarData.OutOfOfficeNote.Text(now); oof != "" && p.CalendarData.IsOutOfOffice {
		lines = append(lines, "Out of office: "+oof)
	}
	if len(lines) == 1 {
		return mp, status
	}
	return mp, strings.Join(lines, "\n")
}

// mapTeamsPresence converts a Teams availability and activity to a Matrix
// presence and status message.  Matrix has only online, unavailable and
// offline, so the status message carries what Teams says beyond that.
func mapTeamsPresence(availability, activity string) (event.Presence, string) {
	var p event.Presence
	var status string
	switch availability {
	case "Available":
		p = event.PresenceOnline
	case "Busy", "BusyIdle":
		p, status = event.PresenceOnline, "Busy"
	case "DoNotDisturb":
		p, status = event.PresenceUnavailable, "Do Not Disturb"
	case "Away", "AvailableIdle":
		p, status = event.PresenceUnavailable, "Away"
	case "BeRightBack":
		p, status = event.PresenceUnavailable, "Be Right Back"
	default:
		p = event.PresenceOffline
	}
	switch activity {
	case "InAMeeting":
		status = "In a meeting"
	case "InACall", "InAConferenceCall":
		status = "In a call"
	case "Presenting":
		status = "Presenting"
	case "OutOfOffice":
		status = "Out of office"
	}
	return p, status
}

// ensurePresenceToken returns a token for the Teams presence service; see
// resourceToken.
func (c *TeamsClient) ensurePresenceToken(ctx context.Context) (string, bool, error) {
	return c.resourceToken(ctx, presence.Scope)
}

// presenceRequest is mautrix.ReqPresence without omitempty on the status
// message: Synapse keeps the previous message when the field is left out,
// so going from "In a meeting" to plain Available or Offline must send "".
type presenceRequest struct {
	Presence  event.Presence `json:"presence"`
	StatusMsg string         `json:"status_msg"`
}

// setPresence sets cli's user's presence and status message, an empty
// message clearing the old one.
func setPresence(ctx context.Context, cli *mautrix.Client, p event.Presence, statusMsg string) error {
	u := cli.BuildClientURL("v3", "presence", cli.UserID, "status")
	_, err := cli.MakeRequest(ctx, http.MethodPut, u, presenceRequest{Presence: p, StatusMsg: statusMsg}, nil)
	return err
}

// pollPresence fetches presence for known ghost users from the Teams
// presence service and pushes changes to Matrix.
func (c *TeamsClient) pollPresence(ctx context.Context) {
	if c == nil || c.Meta == nil || c.Login == nil || c.Main == nil || c.Main.Bridge == nil {
		return
	}
	if c.presenceForbidden.Load() {
		return
	}
	log := zerolog.Ctx(ctx)

	token, fresh, err := c.ensurePresenceToken(ctx)
	if err != nil {
		log.Debug().Err(err).Msg("Skipping presence poll: no presence token")
		return
	}

	// Seed from users we have already seen: enterprise conversations carry no
	// member list, so knownUsers would otherwise stay empty.
	c.seedKnownUsersFromProfiles(ctx)

	// The service takes MRIs; only organisation users have presence.
	c.knownUsersMu.Lock()
	mris := make([]string, 0, len(c.knownUsers))
	for uid := range c.knownUsers {
		if strings.HasPrefix(uid, "8:orgid:") {
			mris = append(mris, uid)
		}
	}
	c.knownUsersMu.Unlock()
	// One's own, for publishOwnPresence.
	self := c.selfTeamsUserID()
	if strings.HasPrefix(self, "8:orgid:") && !slices.Contains(mris, self) {
		mris = append(mris, self)
	}
	if len(mris) == 0 {
		return
	}

	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return
	}
	pc := &presence.Client{HTTP: httpClient, Token: token}

	for i := 0; i < len(mris); i += presenceBatchSize {
		batch := mris[i:min(i+presenceBatchSize, len(mris))]
		states, err := pc.Get(ctx, batch)
		if errors.Is(err, presence.ErrUnauthorized) {
			if fresh {
				// A token issued just now and rejected: the service will not
				// take this login's tokens, so stop asking.
				if c.presenceForbidden.CompareAndSwap(false, true) {
					log.Info().Msg("Presence polling disabled: the Teams presence service rejects this login's token")
				}
			} else {
				c.dropResourceToken(presence.Scope)
			}
			return
		}
		if err != nil {
			log.Debug().Err(err).Msg("Presence request failed")
			continue
		}

		c.presenceMu.Lock()
		if c.presenceCache == nil {
			c.presenceCache = make(map[string]presence.Presence)
		}
		for uid, p := range states {
			if prev, ok := c.presenceCache[uid]; ok && prev == p {
				continue
			}
			c.presenceCache[uid] = p
			if uid == self {
				if err := c.publishOwnPresence(ctx, p); err != nil {
					log.Debug().Err(err).Msg("Failed to publish own presence to account data")
				}
			}
			if err := c.pushGhostPresence(ctx, uid, p); err != nil {
				log.Debug().Err(err).Str("user_id", uid).Msg("Failed to set ghost presence")
				delete(c.presenceCache, uid)
			}
		}
		c.presenceMu.Unlock()
	}
}

// keepPresenceAlive sends again the presence of every ghost not offline,
// before Synapse times it out (see presenceKeepAliveInterval).
func (c *TeamsClient) keepPresenceAlive(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil {
		return
	}
	c.presenceMu.Lock()
	defer c.presenceMu.Unlock()
	for uid, p := range c.presenceCache {
		if mp, _ := presenceStatus(p, time.Now()); mp == event.PresenceOffline {
			continue
		}
		if err := c.pushGhostPresence(ctx, uid, p); err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Str("user_id", uid).Msg("Failed to refresh ghost presence")
			// Sent afresh by the next poll.
			delete(c.presenceCache, uid)
		}
	}
}

// pushGhostPresence sets the Matrix presence of uid's ghost from Teams presence p.
func (c *TeamsClient) pushGhostPresence(ctx context.Context, uid string, p presence.Presence) error {
	ghost, err := c.Main.Bridge.GetGhostByID(ctx, teamsUserIDToNetworkUserID(uid))
	if err != nil {
		return err
	}
	if ghost == nil {
		return errors.New("no ghost")
	}
	// bridgev2.MatrixAPI doesn't expose presence, so we type-assert to the
	// concrete matrix.ASIntent to access the raw mautrix client.
	asIntent, ok := ghost.Intent.(*matrix.ASIntent)
	if !ok {
		return nil
	}
	matrixPresence, statusMsg := presenceStatus(p, time.Now())
	return setPresence(ctx, asIntent.Matrix.Client, matrixPresence, statusMsg)
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
