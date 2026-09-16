package connector

import (
	"context"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

// syncGhostName makes the Matrix ghost for teamsUserID carry displayName.
//
// A ghost is named when it is first needed, and a reaction or read receipt
// can need it before any message from that user has told us their name.
// GetUserInfo then falls back to the raw Teams ID, and because bridgev2
// treats a set name as final, the ID would stick even after the profile
// table learns the real name.  No-op when the name is unknown or unchanged,
// or when the ghost does not exist yet (GetUserInfo names it on creation).
func (c *TeamsClient) syncGhostName(ctx context.Context, teamsUserID, displayName string) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil {
		return
	}
	teamsUserID = strings.TrimSpace(teamsUserID)
	displayName = strings.TrimSpace(displayName)
	if teamsUserID == "" || displayName == "" || displayName == teamsUserID {
		return
	}
	ghost, err := c.Main.Bridge.GetExistingGhostByID(ctx, teamsUserIDToNetworkUserID(teamsUserID))
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("teams_user_id", teamsUserID).Msg("Failed to look up ghost for name sync")
		return
	}
	if ghost == nil || (ghost.Name == displayName && ghost.NameSet) {
		return
	}
	zerolog.Ctx(ctx).Info().
		Str("teams_user_id", teamsUserID).
		Str("old_name", ghost.Name).
		Str("new_name", displayName).
		Msg("Renaming ghost to match its profile")
	ghost.UpdateInfo(ctx, &bridgev2.UserInfo{Name: &displayName})
}

// syncGhostNamesFromProfiles renames every ghost whose name lags the profile
// table.  Run after connecting, it repairs ghosts that were named before
// their profile was known.
func (c *TeamsClient) syncGhostNamesFromProfiles(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil {
		return
	}
	profiles, err := c.Main.DB.Profile.List(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to list profiles for ghost name sync")
		return
	}
	for _, p := range profiles {
		if ctx.Err() != nil {
			return
		}
		c.syncGhostName(ctx, p.TeamsUserID, p.DisplayName)
	}
}
