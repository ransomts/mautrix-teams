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
		c.reportTokenError(err)
		return err
	}

	log.Debug().Msg("Refreshing thread list from Teams API")
	convs, err := c.getAPI().ListConversations(ctx, c.skypeToken())
	c.noteTeamsResult(err)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list conversations")
		return err
	}
	log.Debug().Int("conversations", len(convs)).Msg("Fetched conversations from Teams")
	selfID := c.selfTeamsUserID()

	for _, conv := range convs {
		thread, ok := conv.NormalizeForSelf(selfID)
		if !ok || strings.TrimSpace(thread.ID) == "" || strings.TrimSpace(thread.ConversationID) == "" {
			continue
		}
		// Teams internal system streams (drafts, mentions, annotations, call
		// logs, ...) are not real conversations; don't surface new portals for
		// them. The self-chat "notes" is a real chat and is kept.
		if isNonPollableSystemStream(thread.ID) {
			continue
		}
		// Enterprise conversations don't include member data; resolve DM
		// names from the profile table (or Graph) using the thread ID.
		if thread.IsOneToOne && thread.RoomName == "" {
			thread.RoomName = c.resolveDMNameFromThreadID(ctx, thread.ID)
		}
		// The conversation list names channels and unnamed group chats
		// "Chat"; the real names ("Team / Channel", "Group: A, B", "DM: X")
		// are worked out afterwards and stored.  Never let the generic name
		// from the API, or a stale placeholder, replace a stored real name:
		// that made every restart rename each channel three times.
		storedName := thread.RoomName
		if existing, _ := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, thread.ID); existing != nil {
			storedName = chooseStoredName(thread.RoomName, existing.Name)
		}
		if err := c.Main.DB.ThreadState.Upsert(ctx, &teamsdb.ThreadState{
			BridgeID:     c.Main.Bridge.ID,
			UserLoginID:  c.Login.ID,
			ThreadID:     thread.ID,
			Conversation: thread.ConversationID,
			IsOneToOne:   thread.IsOneToOne,
			Name:         storedName,
		}); err != nil {
			// Without a row the thread is not polled; the next discovery
			// tries again.
			log.Warn().Err(err).Str("thread_id", thread.ID).Msg("Failed to save discovered thread")
		}

		name := storedName
		roomType := ptrRoomType(thread.IsOneToOne)
		chatInfo := &bridgev2.ChatInfo{Type: roomType, CanBackfill: true}
		// A generic name is never announced: an existing portal keeps the
		// name it has until the naming passes below find a real one, and a
		// new portal is named from thread state when its room is created.
		if !isGenericChatName(name) {
			chatInfo.Name = &name
		}

		// Sync topic from conversation properties.
		if topic := conv.ResolveTopic(); topic != "" {
			chatInfo.Topic = &topic
		}

		// Sync member list from conversation data; a DM without member data
		// still has both members in its thread ID.
		members := c.buildChatMemberList(conv, thread.IsOneToOne)
		if members == nil && thread.IsOneToOne {
			members = c.dmMemberListFromThreadID(thread.ID)
		}
		if members != nil {
			chatInfo.Members = members
		}

		// Discovery runs every 30s; only re-announce a chat whose name, topic
		// or membership changed (or that we have not announced since startup),
		// otherwise every pass floods each portal's event queue.
		sig := chatInfoSignature(chatInfo)
		prevSig, changed := c.chatInfoChangedWithPrev(thread.ID, sig)
		if !changed {
			continue
		}
		if prevSig != "" {
			log.Debug().Str("thread_id", thread.ID).Str("previous", prevSig).Str("current", sig).Msg("Chat info changed; announcing")
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

// chatInfoSignature is a canonical rendering of the parts of a ChatInfo that
// discovery can change: name, topic, room type and member IDs.
func chatInfoSignature(info *bridgev2.ChatInfo) string {
	if info == nil {
		return ""
	}
	var b strings.Builder
	if info.Name != nil {
		b.WriteString(*info.Name)
	}
	b.WriteString("\x00")
	if info.Topic != nil {
		b.WriteString(*info.Topic)
	}
	b.WriteString("\x00")
	if info.Type != nil {
		b.WriteString(string(*info.Type))
	}
	b.WriteString("\x00")
	if info.Members != nil {
		ids := make([]string, 0, len(info.Members.MemberMap))
		for id := range info.Members.MemberMap {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		b.WriteString(strings.Join(ids, ","))
	}
	return b.String()
}

// chatInfoChanged records sig for threadID and reports whether it differs
// from the last recorded one; the first sighting counts as a change.
func (c *TeamsClient) chatInfoChanged(threadID string, sig string) bool {
	_, changed := c.chatInfoChangedWithPrev(threadID, sig)
	return changed
}

// chatInfoChangedWithPrev is chatInfoChanged that also returns the previous
// signature (empty when unknown), for diagnostics.
func (c *TeamsClient) chatInfoChangedWithPrev(threadID string, sig string) (string, bool) {
	c.chatInfoMu.Lock()
	defer c.chatInfoMu.Unlock()
	if c.chatInfoSigs == nil {
		c.chatInfoSigs = make(map[string]stampedSig)
	}
	prev, known := c.chatInfoSigs[threadID]
	// Discovery checks every listed thread each pass, which keeps the
	// entries of live threads from expiring.
	c.chatInfoSigs[threadID] = stampedSig{sig: sig, at: time.Now()}
	return prev.sig, !known || prev.sig != sig
}

// dmCounterpartFromThreadID returns the Teams user ID of the other member
// of an enterprise DM thread ("19:UUID1_UUID2@unq.gbl.spaces"), or "".
func (c *TeamsClient) dmCounterpartFromThreadID(threadID string) string {
	if c == nil || c.Meta == nil {
		return ""
	}
	id := strings.TrimPrefix(strings.TrimSpace(threadID), "19:")
	if atIdx := strings.Index(id, "@"); atIdx > 0 {
		id = id[:atIdx]
	}
	parts := strings.SplitN(id, "_", 2)
	if len(parts) != 2 {
		return ""
	}
	// Self user ID is like "8:orgid:UUID" — extract just the UUID part.
	selfUUID := c.selfTeamsUserID()
	if idx := strings.LastIndex(selfUUID, ":"); idx >= 0 {
		selfUUID = selfUUID[idx+1:]
	}
	otherUUID := parts[0]
	if strings.EqualFold(otherUUID, selfUUID) {
		otherUUID = parts[1]
	}
	if otherUUID == "" {
		return ""
	}
	return "8:orgid:" + otherUUID
}

// resolveDMNameFromThreadID returns the display name of the other member of
// an enterprise DM thread: from the profile table, else from Graph (which
// also fills the profile table and renames the ghost), else "".
func (c *TeamsClient) resolveDMNameFromThreadID(ctx context.Context, threadID string) string {
	if c == nil || c.Main == nil || c.Main.DB == nil {
		return ""
	}
	otherUserID := c.dmCounterpartFromThreadID(threadID)
	if otherUserID == "" {
		return ""
	}
	return c.resolveUserDisplayName(ctx, otherUserID)
}

// resolveUserDisplayName returns teamsUserID's display name from the profile
// table, falling back to a Graph directory lookup.  A name Graph returns is
// stored and applied to the ghost.  Returns "" for a user Graph does not
// know either (typically an account that has since been deleted).
func (c *TeamsClient) resolveUserDisplayName(ctx context.Context, teamsUserID string) string {
	name, fromGraph := c.lookupUserDisplayName(ctx, teamsUserID)
	if fromGraph {
		c.syncGhostName(ctx, teamsUserID, name)
	}
	return name
}

// lookupUserDisplayName is resolveUserDisplayName without the ghost update,
// for use from inside GetUserInfo (which is itself the ghost update).  The
// second result says whether the name came from Graph, and so is new.
func (c *TeamsClient) lookupUserDisplayName(ctx context.Context, teamsUserID string) (string, bool) {
	if c == nil || c.Main == nil || c.Main.DB == nil {
		return "", false
	}
	teamsUserID = strings.TrimSpace(teamsUserID)
	if teamsUserID == "" {
		return "", false
	}
	if profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, teamsUserID); err == nil && profile != nil {
		if name := strings.TrimSpace(profile.DisplayName); name != "" && name != teamsUserID {
			return name, false
		}
	}
	// Only directory users ("8:orgid:UUID") can be looked up in Graph, and
	// only when the Graph token is usable.
	uuid, ok := strings.CutPrefix(teamsUserID, "8:orgid:")
	if !ok || uuid == "" || c.recentNameLookupMiss(teamsUserID) {
		return "", false
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		return "", false
	}
	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return "", false
	}
	user, err := gc.GetUserByEmail(ctx, uuid) // accepts an object ID too
	if err != nil {
		log := c.log()
		log.Debug().Err(err).Str("teams_user_id", teamsUserID).Msg("Graph user lookup failed")
		return "", false
	}
	if user == nil || strings.TrimSpace(user.DisplayName) == "" {
		// A deleted account: Graph will not know them next time either.
		c.recordNameLookupMiss(teamsUserID)
		return "", false
	}
	name := strings.TrimSpace(user.DisplayName)
	_ = c.Main.DB.Profile.Upsert(ctx, teamsUserID, name, time.Now().UTC())
	return name, true
}

// nameLookupMissTTL is how long a user Graph does not know is not asked
// about again; discovery would otherwise ask every pass.
const nameLookupMissTTL = 6 * time.Hour

func (c *TeamsClient) recentNameLookupMiss(teamsUserID string) bool {
	c.nameLookupMu.Lock()
	defer c.nameLookupMu.Unlock()
	at, ok := c.nameLookupMiss[teamsUserID]
	return ok && time.Since(at) < nameLookupMissTTL
}

func (c *TeamsClient) recordNameLookupMiss(teamsUserID string) {
	c.nameLookupMu.Lock()
	defer c.nameLookupMu.Unlock()
	if c.nameLookupMiss == nil {
		c.nameLookupMiss = make(map[string]time.Time)
	}
	c.nameLookupMiss[teamsUserID] = time.Now()
}

// dmMemberListFromThreadID builds the member list of an enterprise DM from
// its thread ID, for conversations the API returns without member data, so
// the counterpart's ghost is in the room even before it has ever spoken.
func (c *TeamsClient) dmMemberListFromThreadID(threadID string) *bridgev2.ChatMemberList {
	other := c.dmCounterpartFromThreadID(threadID)
	if other == "" || c.Meta == nil || c.Login == nil {
		return nil
	}
	selfID := model.NormalizeTeamsUserID(c.selfTeamsUserID())
	c.trackKnownUser(other)
	memberMap := make(bridgev2.ChatMemberMap)
	memberMap.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{IsFromMe: true, SenderLogin: c.Login.ID, Sender: teamsUserIDToNetworkUserID(selfID)},
		Membership:  event.MembershipJoin,
	})
	memberMap.Set(bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(other)},
		Membership:  event.MembershipJoin,
	})
	return &bridgev2.ChatMemberList{
		IsFull:                     true,
		CheckAllLogins:             true,
		MemberMap:                  memberMap,
		TotalMemberCount:           len(memberMap),
		ExcludeChangesFromTimeline: true,
	}
}

// resolveUnnamedGroupChats updates group chat threads named "Chat" by building
// a participant-list name from the sender profiles of ingested messages.
// Only real group chats: channels, meetings and system streams get their
// names elsewhere and must not be given a member list as a name.
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
		selfID = c.selfTeamsUserID()
	}
	for _, th := range threads {
		if th.IsOneToOne || !isGenericChatName(th.Name) || !isGroupChatThread(th.ThreadID) {
			continue
		}
		name := c.buildGroupChatName(ctx, th.ThreadID, selfID)
		var members *bridgev2.ChatMemberList
		if name == "" {
			// Nobody but us has spoken: ask the chat service who is in it.
			name, members = c.groupChatNameAndMembers(ctx, th.ThreadID, selfID)
		}
		if name == "" || name == th.Name {
			continue
		}
		th.Name = name
		_ = c.Main.DB.ThreadState.Upsert(ctx, th)
		chatInfo := &bridgev2.ChatInfo{Name: &name, Members: members}
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
	return joinGroupNames(names)
}

// joinGroupNames renders a group chat's participant names as its name:
// sorted, comma-separated, at most three before "+N others".
func joinGroupNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	names = append([]string(nil), names...)
	sort.Strings(names)
	if len(names) > 4 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d others", len(names)-3)
	}
	return strings.Join(names, ", ")
}

// groupChatNameAndMembers names a group chat from the chat service's member
// list and returns that list for the portal, for chats where nobody but the
// user has sent a message (so message senders reveal nothing).  Members
// whose name cannot be resolved still join, under their ID.
func (c *TeamsClient) groupChatNameAndMembers(ctx context.Context, threadID string, selfUserID string) (string, *bridgev2.ChatMemberList) {
	if c == nil || c.Login == nil {
		return "", nil
	}
	ids, err := c.getAPI().GetThreadMembers(ctx, threadID)
	if err != nil {
		log := c.log()
		log.Debug().Err(err).Str("thread_id", threadID).Msg("Failed to fetch thread members")
		return "", nil
	}
	selfNorm := model.NormalizeTeamsUserID(selfUserID)
	memberMap := make(bridgev2.ChatMemberMap)
	var names []string
	for _, id := range ids {
		id = model.NormalizeTeamsUserID(id)
		if id == "" || strings.HasPrefix(strings.ToLower(id), "28:") {
			continue // bots
		}
		es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(id)}
		if id == selfNorm {
			es.IsFromMe = true
			es.SenderLogin = c.Login.ID
		} else {
			c.trackKnownUser(id)
			if name := c.resolveUserDisplayName(ctx, id); name != "" {
				names = append(names, name)
			}
		}
		memberMap.Set(bridgev2.ChatMember{EventSender: es, Membership: event.MembershipJoin})
	}
	var members *bridgev2.ChatMemberList
	if len(memberMap) > 0 {
		members = &bridgev2.ChatMemberList{
			IsFull:                     true,
			CheckAllLogins:             true,
			MemberMap:                  memberMap,
			TotalMemberCount:           len(memberMap),
			ExcludeChangesFromTimeline: true,
		}
	}
	return joinGroupNames(names), members
}

// applyStructuredRoomNames adds type prefixes (DM:, Group:, Meeting:) to room
// names, resolves Team->Channel hierarchy via Graph API, and assigns clean
// names to system streams.
func (c *TeamsClient) applyStructuredRoomNames(ctx context.Context, channelMap map[string]graph.ChannelInfo) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Login == nil {
		return
	}
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		return
	}

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
				// Nobody (profile table, Graph) knows the counterpart, which
				// happens for accounts that have since been deleted.  Name
				// the room after the ID rather than leaving it nameless,
				// which showed it as "bridge bot, <me>".
				baseName = c.resolveDMNameFromThreadID(ctx, threadID)
				if baseName == "" {
					newName = placeholderDMName(c.dmCounterpartFromThreadID(threadID))
					if newName == "" {
						continue
					}
					break
				}
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
		// Set parent space and topic (channel description) for channels.
		if strings.Contains(threadID, "@thread.tacv2") {
			if info, ok := channelMap[threadID]; ok {
				if info.TeamID != "" {
					parentID := teamPortalID(info.TeamID)
					chatInfo.ParentID = &parentID
				}
				if desc := strings.TrimSpace(info.Description); desc != "" {
					chatInfo.Topic = &desc
				}
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
	graphToken, err := c.graphAccessToken()
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
		selfID = model.NormalizeTeamsUserID(c.selfTeamsUserID())
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

var typePrefixes = []string{
	"DM: ", "Group: ", "Meeting: ",
	// Legacy bracket prefixes from previous versions.
	"[DM] ", "[Channel] ", "[Group] ", "[Meeting] ",
}

func hasTypePrefix(name string) bool {
	for _, prefix := range typePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// stripTypePrefix returns name without its "DM: "/"Group: "/… prefix.
func stripTypePrefix(name string) string {
	for _, prefix := range typePrefixes {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			return rest
		}
	}
	return name
}

// chooseStoredName decides the name to store for a thread from the name the
// conversation list reports (apiName) and the one stored last time.
func chooseStoredName(apiName, existingName string) string {
	switch {
	case existingName == "":
		return apiName
	case isGenericChatName(apiName) && !isGenericChatName(existingName):
		// The API has nothing better than "Chat": keep what was worked
		// out (a structured, prefixed or placeholder name).
		return existingName
	case hasTypePrefix(existingName) && !isPlaceholderDMName(existingName) &&
		apiName == stripTypePrefix(existingName):
		// Same underlying name as before: keep the prefixed form.  A
		// different one (a renamed chat, a person's new display name)
		// goes through and is prefixed again.
		return existingName
	case strings.HasSuffix(existingName, " / "+apiName):
		// A channel: the API gives the bare channel name, the stored one
		// is "Team / Channel".
		return existingName
	}
	return apiName
}

// isGenericChatName reports whether name is the API's stand-in for "no
// name" rather than something a person chose.
func isGenericChatName(name string) bool {
	name = strings.TrimSpace(name)
	return name == "" || name == "Chat"
}

const placeholderDMPrefix = "DM: unknown user "

// placeholderDMName names a DM whose counterpart cannot be resolved, after
// the first block of their ID; "" when there is no ID either.
func placeholderDMName(teamsUserID string) string {
	id := strings.TrimPrefix(strings.TrimSpace(teamsUserID), "8:orgid:")
	if id == "" {
		return ""
	}
	if idx := strings.Index(id, "-"); idx > 0 {
		id = id[:idx]
	}
	return placeholderDMPrefix + id
}

func isPlaceholderDMName(name string) bool {
	return strings.HasPrefix(name, placeholderDMPrefix)
}

// isGroupChatThread reports whether threadID is a plain group chat: not a
// DM, channel, meeting or system stream.
func isGroupChatThread(threadID string) bool {
	threadID = strings.ToLower(strings.TrimSpace(threadID))
	return strings.HasSuffix(threadID, "@thread.v2") &&
		!strings.Contains(threadID, "meeting_") &&
		!strings.Contains(threadID, "teamsstream_")
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
	case strings.Contains(threadID, "teamsstream_drafts"):
		return "Drafts"
	case strings.Contains(threadID, "teamsstream_mentions"):
		return "Mentions"
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

// syncTeamSpaces emits ChatResync events for each Team as a Matrix space. Team
// names come from me/joinedTeams and, as a fallback, from CHANNELMAP (which
// carries the team name for every discovered channel) so a team whose channels
// the user sees but which me/joinedTeams omits still gets a proper name instead
// of "Chat"/"Team". Resolved names are cached for teamSpaceChatInfo.
func (c *TeamsClient) syncTeamSpaces(ctx context.Context, channelMap map[string]graph.ChannelInfo) {
	if c == nil || c.Meta == nil || c.Login == nil {
		return
	}
	names := make(map[string]string)
	if gc, err := c.getGraphClient(ctx); err == nil {
		if teams, err := gc.ListJoinedTeams(ctx); err == nil {
			for _, team := range teams {
				if id := strings.TrimSpace(team.ID); id != "" {
					if dn := strings.TrimSpace(team.DisplayName); dn != "" {
						names[id] = dn
					} else if _, ok := names[id]; !ok {
						names[id] = ""
					}
				}
			}
		} else {
			zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to fetch joined teams for space sync")
		}
	}
	// Merge team names discovered via channels (only fill gaps).
	for _, info := range channelMap {
		id := strings.TrimSpace(info.TeamID)
		if id == "" {
			continue
		}
		if names[id] == "" {
			if tn := strings.TrimSpace(info.TeamName); tn != "" {
				names[id] = tn
			}
		}
	}
	if len(names) == 0 {
		return
	}
	c.cacheTeamNames(names)
	spaceType := database.RoomTypeSpace
	for teamID, teamName := range names {
		name := teamName
		if name == "" {
			name = "Team"
		}
		chatInfo := &bridgev2.ChatInfo{Name: &name, Type: &spaceType}
		c.queueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				// Unscoped receiver: channels reference their parent space with
				// an empty receiver, so the space portal must be unscoped too,
				// otherwise a second (orphan, login-scoped) space is created.
				PortalKey:    networkid.PortalKey{ID: teamPortalID(teamID)},
				CreatePortal: true,
				Timestamp:    time.Now().UTC(),
			},
			ChatInfo: chatInfo,
		})
	}
}

// cacheTeamNames merges resolved team display names into the client cache.
func (c *TeamsClient) cacheTeamNames(names map[string]string) {
	c.teamNamesMu.Lock()
	defer c.teamNamesMu.Unlock()
	if c.teamNames == nil {
		c.teamNames = make(map[string]string)
	}
	for id, name := range names {
		if strings.TrimSpace(name) != "" {
			c.teamNames[id] = name
		}
	}
}

// cachedTeamName returns a cached team display name, or "".
func (c *TeamsClient) cachedTeamName(teamID string) string {
	c.teamNamesMu.Lock()
	defer c.teamNamesMu.Unlock()
	return c.teamNames[teamID]
}
