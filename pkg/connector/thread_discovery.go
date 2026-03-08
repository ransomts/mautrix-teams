package connector

// Thread discovery, naming, and member list resolution.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

func (c *TeamsClient) refreshThreads(ctx context.Context) error {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return nil
	}
	log := c.log()
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Message: err.Error(), UserAction: status.UserActionRelogin})
		return err
	}

	log.Debug().Msg("Refreshing thread list from Teams API")
	convs, err := c.getAPI().ListConversations(ctx, c.Meta.SkypeToken)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list conversations")
		return err
	}
	log.Debug().Int("conversations", len(convs)).Msg("Fetched conversations from Teams")

	for _, conv := range convs {
		thread, ok := conv.NormalizeForSelf(c.Meta.TeamsUserID)
		if !ok || strings.TrimSpace(thread.ID) == "" || strings.TrimSpace(thread.ConversationID) == "" {
			continue
		}
		// Enterprise conversations don't include member data; resolve DM
		// names from the profile table using the thread ID.
		if thread.IsOneToOne && thread.RoomName == "" {
			thread.RoomName = c.resolveDMNameFromThreadID(ctx, thread.ID)
		}
		// Preserve prefixed names from applyStructuredRoomNames — don't
		// overwrite a "DM: ..." or "Group: ..." name with the raw name.
		storedName := thread.RoomName
		if existing, _ := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, thread.ID); existing != nil {
			if hasTypePrefix(existing.Name) {
				storedName = existing.Name
			}
		}
		_ = c.Main.DB.ThreadState.Upsert(ctx, &teamsdb.ThreadState{
			BridgeID:     c.Main.Bridge.ID,
			UserLoginID:  c.Login.ID,
			ThreadID:     thread.ID,
			Conversation: thread.ConversationID,
			IsOneToOne:   thread.IsOneToOne,
			Name:         storedName,
		})

		name := storedName
		roomType := ptrRoomType(thread.IsOneToOne)
		chatInfo := &bridgev2.ChatInfo{Name: &name, Type: roomType, CanBackfill: true}

		// Sync topic from conversation properties.
		if topic := conv.ResolveTopic(); topic != "" {
			chatInfo.Topic = &topic
		}

		// Sync member list from conversation data.
		if members := c.buildChatMemberList(conv, thread.IsOneToOne); members != nil {
			chatInfo.Members = members
		}

		c.queueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    c.portalKey(thread.ID),
				CreatePortal: true,
				Timestamp:    time.Now().UTC(),
			},
			ChatInfo: chatInfo,
		})
	}
	return nil
}

// resolveDMNameFromThreadID extracts the other participant's display name
// from an enterprise DM thread ID like "19:UUID1_UUID2@unq.gbl.spaces".
func (c *TeamsClient) resolveDMNameFromThreadID(ctx context.Context, threadID string) string {
	if c == nil || c.Meta == nil || c.Main == nil || c.Main.DB == nil {
		return ""
	}
	// Thread ID format: "19:UUID1_UUID2@unq.gbl.spaces"
	id := strings.TrimPrefix(threadID, "19:")
	if atIdx := strings.Index(id, "@"); atIdx > 0 {
		id = id[:atIdx]
	}
	parts := strings.SplitN(id, "_", 2)
	if len(parts) != 2 {
		return ""
	}
	// Self user ID is like "8:orgid:UUID" — extract just the UUID part.
	selfUUID := c.Meta.TeamsUserID
	if idx := strings.LastIndex(selfUUID, ":"); idx >= 0 {
		selfUUID = selfUUID[idx+1:]
	}
	otherUUID := parts[0]
	if strings.EqualFold(otherUUID, selfUUID) {
		otherUUID = parts[1]
	}
	otherUserID := "8:orgid:" + otherUUID
	profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, otherUserID)
	if err != nil || profile == nil || strings.TrimSpace(profile.DisplayName) == "" {
		return ""
	}
	return profile.DisplayName
}

// resolveUnnamedGroupChats updates group chat threads named "Chat" by building
// a participant-list name from the sender profiles of ingested messages.
func (c *TeamsClient) resolveUnnamedGroupChats(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Main.Bridge == nil || c.Login == nil {
		return
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return
	}
	selfID := ""
	if c.Meta != nil {
		selfID = c.Meta.TeamsUserID
	}
	for _, th := range threads {
		if th.IsOneToOne || (th.Name != "Chat" && th.Name != "") {
			continue
		}
		name := c.buildGroupChatName(ctx, th.ThreadID, selfID)
		if name == "" || name == th.Name {
			continue
		}
		th.Name = name
		_ = c.Main.DB.ThreadState.Upsert(ctx, th)
		chatInfo := &bridgev2.ChatInfo{Name: &name}
		c.queueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    c.portalKey(th.ThreadID),
				CreatePortal: false,
				Timestamp:    time.Now().UTC(),
			},
			ChatInfo: chatInfo,
		})
	}
}

// buildGroupChatName queries distinct message senders for a thread and builds
// a comma-separated participant name like "Alice, Bob, Charlie".
func (c *TeamsClient) buildGroupChatName(ctx context.Context, threadID string, selfUserID string) string {
	rows, err := c.Main.Bridge.DB.Query(ctx, `
		SELECT DISTINCT m.sender_id
		FROM message m
		WHERE m.bridge_id=$1 AND m.room_id=$2
	`, c.Main.Bridge.DB.BridgeID, networkid.PortalID(threadID))
	if err != nil {
		return ""
	}
	defer rows.Close()
	var names []string
	selfNorm := strings.ToLower(strings.TrimSpace(selfUserID))
	for rows.Next() {
		var senderID string
		if err := rows.Scan(&senderID); err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(senderID)) == selfNorm {
			continue
		}
		profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, senderID)
		if err != nil || profile == nil || strings.TrimSpace(profile.DisplayName) == "" {
			continue
		}
		names = append(names, profile.DisplayName)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	if len(names) > 4 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d others", len(names)-3)
	}
	return strings.Join(names, ", ")
}

// applyStructuredRoomNames adds type prefixes (DM:, Group:, Meeting:) to room
// names, resolves Team->Channel hierarchy via Graph API, and assigns clean
// names to system streams.
func (c *TeamsClient) applyStructuredRoomNames(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return
	}

	// Fetch team/channel mapping from Graph API.
	channelMap := c.fetchTeamChannelMap(ctx)

	for _, th := range threads {
		threadID := strings.TrimSpace(th.ThreadID)
		if threadID == "" {
			continue
		}
		// Name system streams cleanly instead of skipping them.
		if strings.Contains(threadID, "teamsstream_") {
			newName := systemStreamName(threadID)
			if newName == "" || newName == th.Name {
				continue
			}
			th.Name = newName
			_ = c.Main.DB.ThreadState.Upsert(ctx, th)
			chatInfo := &bridgev2.ChatInfo{Name: &newName}
			c.queueRemoteEvent(&simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    c.portalKey(threadID),
					CreatePortal: false,
					Timestamp:    time.Now().UTC(),
				},
				ChatInfo: chatInfo,
			})
			continue
		}

		// Skip if already prefixed from a previous cycle.
		if hasTypePrefix(th.Name) {
			continue
		}

		baseName := th.Name
		if baseName == "" || baseName == "Chat" {
			baseName = ""
		}

		var newName string
		switch {
		case th.IsOneToOne:
			if baseName == "" {
				continue
			}
			newName = "DM: " + baseName
		case strings.Contains(threadID, "meeting_"):
			if baseName == "" {
				baseName = "Meeting"
			}
			newName = "Meeting: " + baseName
		case strings.Contains(threadID, "@thread.tacv2"):
			if info, ok := channelMap[threadID]; ok {
				teamName := info.TeamName
				channelName := info.ChannelName
				if channelName == "" {
					channelName = baseName
				}
				if teamName != "" && channelName != "" {
					newName = teamName + " / " + channelName
				} else if channelName != "" {
					newName = channelName
				} else if teamName != "" {
					newName = teamName
				} else if baseName != "" {
					newName = baseName
				} else {
					newName = threadID
				}
			} else if baseName != "" {
				newName = baseName
			} else {
				newName = threadID
			}
		default:
			// Group chat (thread.v2, non-meeting, non-system)
			if baseName == "" {
				continue
			}
			newName = "Group: " + baseName
		}

		if newName == th.Name {
			continue
		}
		log := c.log()
		log.Debug().Str("thread_id", threadID).Str("old_name", th.Name).Str("new_name", newName).Msg("Applying structured room name")
		th.Name = newName
		_ = c.Main.DB.ThreadState.Upsert(ctx, th)
		chatInfo := &bridgev2.ChatInfo{Name: &newName}
		// Set parent space for channels.
		if strings.Contains(threadID, "@thread.tacv2") {
			if info, ok := channelMap[threadID]; ok && info.TeamID != "" {
				parentID := teamPortalID(info.TeamID)
				chatInfo.ParentID = &parentID
			}
		}
		c.queueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    c.portalKey(threadID),
				CreatePortal: false,
				Timestamp:    time.Now().UTC(),
			},
			ChatInfo: chatInfo,
		})
	}
}

// fetchTeamChannelMap uses the Graph API to build a map from channel thread ID
// to team+channel display names. Returns an empty map on failure.
func (c *TeamsClient) fetchTeamChannelMap(ctx context.Context) map[string]graph.ChannelInfo {
	if c == nil || c.Meta == nil {
		return nil
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Cannot fetch team/channel names: graph token unavailable")
		return nil
	}
	graphToken, err := c.Meta.GetGraphAccessToken()
	if err != nil {
		return nil
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil
	}
	gc := graph.NewClient(httpClient)
	gc.AccessToken = graphToken
	channelMap, err := gc.ListJoinedTeamsAndChannels(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch team/channel mapping from Graph API")
		return nil
	}
	return channelMap
}

func (c *TeamsClient) buildChatMemberList(conv model.RemoteConversation, isOneToOne bool) *bridgev2.ChatMemberList {
	selfID := ""
	if c.Meta != nil {
		selfID = model.NormalizeTeamsUserID(c.Meta.TeamsUserID)
	}

	memberMap := make(bridgev2.ChatMemberMap)
	for _, list := range [][]model.ConversationMember{conv.Members, conv.Participants, conv.Consumers} {
		for _, m := range list {
			rawID := strings.TrimSpace(m.ID)
			if rawID == "" {
				rawID = strings.TrimSpace(m.MRI)
			}
			if rawID == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(rawID), "28:") {
				continue // skip bots
			}
			// Track for presence polling.
			normalizedMember := model.NormalizeTeamsUserID(rawID)
			c.trackKnownUser(normalizedMember)
			es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(rawID)}
			if selfID != "" && model.NormalizeTeamsUserID(rawID) == selfID {
				es.IsFromMe = true
				es.SenderLogin = c.Login.ID
			}
			memberMap.Set(bridgev2.ChatMember{
				EventSender: es,
				Membership:  event.MembershipJoin,
			})
		}
	}
	if len(memberMap) == 0 {
		return nil
	}
	return &bridgev2.ChatMemberList{
		IsFull:                     true,
		CheckAllLogins:             true,
		MemberMap:                  memberMap,
		TotalMemberCount:           len(memberMap),
		ExcludeChangesFromTimeline: true,
	}
}

func hasTypePrefix(name string) bool {
	return strings.HasPrefix(name, "DM: ") || strings.HasPrefix(name, "Group: ") ||
		strings.HasPrefix(name, "Meeting: ") ||
		// Legacy bracket prefixes from previous versions.
		strings.HasPrefix(name, "[DM] ") || strings.HasPrefix(name, "[Channel] ") ||
		strings.HasPrefix(name, "[Group] ") || strings.HasPrefix(name, "[Meeting] ")
}

// systemStreamName maps a teamsstream_ thread ID to a clean display name.
func systemStreamName(threadID string) string {
	switch {
	case strings.Contains(threadID, "teamsstream_notes"):
		return "Notes"
	case strings.Contains(threadID, "teamsstream_notifications"):
		return "Notifications"
	case strings.Contains(threadID, "teamsstream_calllogs"):
		return "Call Log"
	case strings.Contains(threadID, "teamsstream_annotations"):
		return "Annotations"
	case strings.Contains(threadID, "teamsstream_threads"):
		return "Threads"
	default:
		return ""
	}
}

func ptrRoomType(isOneToOne bool) *database.RoomType {
	t := database.RoomTypeDefault
	if isOneToOne {
		t = database.RoomTypeDM
	}
	return &t
}

// teamPortalID returns a synthetic portal ID for a Teams team (space).
func teamPortalID(teamID string) networkid.PortalID {
	return networkid.PortalID("team:" + strings.TrimSpace(teamID))
}

// syncTeamSpaces emits ChatResync events for each joined Team as a Matrix space.
func (c *TeamsClient) syncTeamSpaces(ctx context.Context) {
	if c == nil || c.Meta == nil {
		return
	}
	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return
	}
	teams, err := gc.ListJoinedTeams(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to fetch joined teams for space sync")
		return
	}

	for _, team := range teams {
		if strings.TrimSpace(team.ID) == "" {
			continue
		}
		portalID := teamPortalID(team.ID)
		spaceType := database.RoomTypeSpace
		name := strings.TrimSpace(team.DisplayName)
		if name == "" {
			name = "Team"
		}
		chatInfo := &bridgev2.ChatInfo{
			Name: &name,
			Type: &spaceType,
		}
		c.queueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    networkid.PortalKey{ID: portalID, Receiver: c.Login.ID},
				CreatePortal: true,
				Timestamp:    time.Now().UTC(),
			},
			ChatInfo: chatInfo,
		})
	}
}
