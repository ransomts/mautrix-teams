package connector

// Team spaces: every channel portal is a child of its team's space portal,
// and the space portal's Matrix room is an m.space.  This file keeps that
// true for portals created before the bridge got it right.

import (
	"context"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
)

const teamPortalPrefix = "team:"

func isTeamPortalID(id networkid.PortalID) bool {
	return strings.HasPrefix(string(id), teamPortalPrefix)
}

// repairTeamSpaces fixes team portals left behind by earlier versions:
//
//   - an unscoped team portal whose Matrix room is a plain room rather than
//     a space (the parent portal was created on demand before the bridge
//     declared team portals to be spaces; a room's type cannot change, and
//     bridgev2 warns "Tried to change existing room type" on every sync).
//     The plain room is cleaned up and detached so the next
//     syncChannelParents pass creates a real space and re-adds the
//     channels.
//   - a login-scoped team portal (receiver set), the orphan twin that
//     syncTeamSpaces used to create; channels reference the unscoped one.
//     Deleted outright, with its room.
func (c *TeamsClient) repairTeamSpaces(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil {
		return
	}
	log := c.log()
	portals, err := c.Main.Bridge.GetAllPortals(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list portals for team space repair")
		return
	}
	for _, portal := range portals {
		if portal == nil || !isTeamPortalID(portal.ID) {
			continue
		}
		plog := log.With().Str("portal_id", string(portal.ID)).Stringer("portal_mxid", portal.MXID).Logger()
		children, err := c.Main.Bridge.GetChildPortals(ctx, portal.PortalKey)
		if err != nil {
			plog.Warn().Err(err).Msg("Failed to list child portals")
			continue
		}
		switch {
		case portal.Receiver != "":
			if len(children) > 0 {
				plog.Warn().Int("children", len(children)).Msg("Login-scoped team portal has children, leaving it alone")
				continue
			}
			plog.Info().Msg("Deleting orphan login-scoped team portal")
			if portal.MXID != "" {
				if err := c.Main.Bridge.Bot.DeleteRoom(ctx, portal.MXID, false); err != nil {
					plog.Warn().Err(err).Msg("Failed to clean up orphan team room")
				}
			}
			if err := portal.Delete(ctx); err != nil {
				plog.Warn().Err(err).Msg("Failed to delete orphan team portal")
			}
		case portal.MXID != "" && portal.RoomType != database.RoomTypeSpace:
			plog.Info().Int("children", len(children)).Msg("Team portal's room is not a space; detaching it so a space can be created")
			if err := c.Main.Bridge.Bot.DeleteRoom(ctx, portal.MXID, false); err != nil {
				plog.Warn().Err(err).Msg("Failed to clean up non-space team room")
			}
			portal.RoomType = database.RoomTypeSpace
			if err := portal.RemoveMXID(ctx); err != nil {
				plog.Warn().Err(err).Msg("Failed to detach non-space team room")
				continue
			}
			// The children were in the old room; make them join the new one.
			for _, child := range children {
				if child.InSpace {
					child.InSpace = false
					if err := child.Save(ctx); err != nil {
						plog.Warn().Err(err).Str("child_id", string(child.ID)).Msg("Failed to mark child portal as not in space")
					}
				}
			}
		}
	}
}

// syncChannelParents makes every channel portal a child of its team's
// space, creating the space when it has no room yet.  Independent of
// naming: applyStructuredRoomNames only sets a parent while renaming, so a
// channel whose name was already right never got (re)parented.
func (c *TeamsClient) syncChannelParents(ctx context.Context, channelMap map[string]graph.ChannelInfo) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil || c.Main.DB == nil || c.Login == nil || len(channelMap) == 0 {
		return
	}
	log := c.log()
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return
	}
	for _, th := range threads {
		if ctx.Err() != nil {
			return
		}
		threadID := strings.TrimSpace(th.ThreadID)
		info, ok := channelMap[threadID]
		if !ok || strings.TrimSpace(info.TeamID) == "" {
			continue
		}
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(threadID))
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		wantParent := teamPortalID(info.TeamID)
		plog := log.With().Str("portal_id", threadID).Str("parent_id", string(wantParent)).Logger()
		if portal.ParentKey.ID != wantParent {
			// bridgev2 moves it between spaces, creating the new one if
			// needed.
			plog.Info().Str("old_parent_id", string(portal.ParentKey.ID)).Msg("Channel portal has the wrong parent; reparenting")
			parentID := wantParent
			c.queueRemoteEvent(&simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventChatResync,
					PortalKey: portal.PortalKey,
					Timestamp: time.Now().UTC(),
				},
				ChatInfo: &bridgev2.ChatInfo{ParentID: &parentID},
			})
			continue
		}
		if portal.InSpace || portal.Parent == nil {
			continue
		}
		pctx := plog.WithContext(ctx)
		if portal.Parent.MXID == "" {
			plog.Info().Msg("Channel portal's team space has no room; creating it and adding the channel")
			// bridgev2 only does this itself when a portal's parent changes or its
			// room is created, and exposes it nowhere but Internal().
			//lint:ignore SA1019 no public alternative for re-adding an existing portal to its space
			portal.Internal().CreateParentAndAddToSpace(pctx, c.Login)
		} else {
			plog.Info().Msg("Channel portal is not in its team space; adding it")
			//lint:ignore SA1019 no public alternative for re-adding an existing portal to its space
			portal.Internal().AddToParentSpaceAndSave(pctx, true)
		}
	}
}
