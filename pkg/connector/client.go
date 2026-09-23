package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	internalbridge "go.mau.fi/mautrix-teams/internal/bridge"
	"go.mau.fi/mautrix-teams/internal/teams/auth"
	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

type TeamsClient struct {
	Main  *TeamsConnector
	Login *bridgev2.UserLogin
	Meta  *teamsid.UserLoginMetadata

	loggedIn atomic.Bool

	apiMu  sync.RWMutex
	api    TeamsAPI
	events EventSink

	// tokenMu serialises token refreshes and guards the token fields of Meta
	// (SkypeToken, RefreshToken, GraphAccessToken and their expiries), which
	// are otherwise written from the sync loop, the presence loop and Matrix
	// event handlers concurrently.
	tokenMu          sync.Mutex
	skypeRefreshFail refreshFailure
	graphRefreshFail refreshFailure
	badCreds         badCredentials // see client_lifecycle.go

	consumerHTTPMu sync.Mutex
	consumerHTTP   *http.Client

	syncMu     sync.Mutex
	syncCancel context.CancelFunc
	syncDone   chan struct{}

	reach teamsReach // whether Teams is answering; see reachability.go

	reactionSeenMu sync.Mutex
	reactionSeen   map[string]struct{}
	reactionSigs   map[string]string // messageID -> last announced reaction signature

	chatInfoMu   sync.Mutex
	chatInfoSigs map[string]string // threadID -> last announced chat info signature

	teamNamesMu sync.Mutex
	teamNames   map[string]string // teamID -> display name (from Graph teams/channels)

	receiptPollMu sync.Mutex
	receiptPoll   map[string]time.Time
	threadActive  map[string]time.Time // threadID -> last own send; see poll_wake.go

	pollWakeOnce  sync.Once
	pollWake      chan pollWakeup // see poll_wake.go
	longPollUp    atomic.Bool     // long-poll notifications are arriving
	activityUp    atomic.Bool     // recent-conversation checks are working
	unreadMu      sync.Mutex
	unreadSeen    map[string]bool
	unreadSent    map[string]bool
	selfMessageMu sync.Mutex
	selfMessages  map[string]time.Time

	presenceMu        sync.Mutex
	presenceCache     map[string]string // userID -> last known availability
	presenceForbidden atomic.Bool       // set when the tenant denies presence (403)

	knownUsersMu sync.Mutex
	knownUsers   map[string]struct{}

	nameLookupMu   sync.Mutex
	nameLookupMiss map[string]time.Time // userID -> when Graph last had no name for them

	typingSeenMu sync.Mutex
	typingSeen   map[string]time.Time // "threadID:senderID" -> last emitted
}

var (
	_ bridgev2.NetworkAPI                    = (*TeamsClient)(nil)
	_ bridgev2.BackgroundSyncingNetworkAPI   = (*TeamsClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*TeamsClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*TeamsClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*TeamsClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*TeamsClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*TeamsClient)(nil)
	_ bridgev2.BackfillingNetworkAPI         = (*TeamsClient)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI    = (*TeamsClient)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI   = (*TeamsClient)(nil)
	_ bridgev2.IdentifierResolvingNetworkAPI = (*TeamsClient)(nil)
	_ bridgev2.UserSearchingNetworkAPI       = (*TeamsClient)(nil)
	_ bridgev2.GroupCreatingNetworkAPI       = (*TeamsClient)(nil)
	_ bridgev2.ContactListingNetworkAPI      = (*TeamsClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI  = (*TeamsClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI  = (*TeamsClient)(nil)
)

func (c *TeamsClient) Connect(ctx context.Context) {
	if c == nil || c.Login == nil || c.Main == nil {
		return
	}
	log := c.log()
	if c.Meta == nil {
		if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok {
			c.Meta = meta
		} else {
			c.Meta = &teamsid.UserLoginMetadata{}
			c.Login.Metadata = c.Meta
		}
	}

	log.Info().Msg("Connecting to Teams")
	c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	if err := c.ensureValidSkypeToken(ctx); err != nil {
		c.loggedIn.Store(false)
		log.Error().Err(err).Msg("Failed to ensure valid Teams tokens")
		c.reportBadCredentials(err)
		return
	}

	c.loggedIn.Store(true)
	c.apiMu.Lock()
	c.api = c.newConsumer()
	c.events = &loginEventSink{login: c.Login}
	c.apiMu.Unlock()
	log.Info().Str("teams_user_id", c.Meta.TeamsUserID).Msg("Connected to Teams")
	c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	// Ghosts named before their profile was known keep the raw Teams ID
	// until something renames them; do that before events start flowing.
	c.syncGhostNamesFromProfiles(ctx)
	c.startSyncLoop()
}

func (c *TeamsClient) Disconnect() {
	c.stopSyncLoop(5 * time.Second)
}

func (c *TeamsClient) IsLoggedIn() bool {
	if c == nil {
		return false
	}
	if c.loggedIn.Load() {
		return true
	}
	if c.Meta == nil {
		if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok {
			c.Meta = meta
		}
	}
	token, expiresAtUnix := c.skypeTokenSnapshot()
	if token == "" || expiresAtUnix == 0 {
		return false
	}
	expiresAt := time.Unix(expiresAtUnix, 0).UTC()
	return time.Now().UTC().Add(auth.SkypeTokenExpirySkew).Before(expiresAt)
}

// skypeTokenSnapshot returns the current Skype token and its expiry under tokenMu.
func (c *TeamsClient) skypeTokenSnapshot() (string, int64) {
	if c == nil {
		return "", 0
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.Meta == nil {
		return "", 0
	}
	return c.Meta.SkypeToken, c.Meta.SkypeTokenExpiresAt
}

// skypeToken returns the current Skype token under tokenMu.
func (c *TeamsClient) skypeToken() string {
	token, _ := c.skypeTokenSnapshot()
	return token
}

func (c *TeamsClient) LogoutRemote(ctx context.Context) {
	if c == nil || c.Login == nil {
		return
	}
	c.stopSyncLoop(5 * time.Second)
	if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok && meta != nil {
		*meta = teamsid.UserLoginMetadata{}
	}
	_ = c.saveLogin(ctx)
	c.loggedIn.Store(false)
}

func (c *TeamsClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	_ = ctx
	if c == nil || c.Meta == nil {
		return false
	}
	return strings.TrimSpace(string(userID)) != "" &&
		teamsUserIDToNetworkUserID(c.Meta.TeamsUserID) == userID
}

func (c *TeamsClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	if c == nil || c.Login == nil || c.Main == nil || c.Main.DB == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	threadID := strings.TrimSpace(string(portal.ID))
	if threadID == "" {
		return nil, errors.New("missing thread id")
	}
	row, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		if teamID, ok := strings.CutPrefix(threadID, "team:"); ok {
			return c.teamSpaceChatInfo(ctx, teamID, portal), nil
		}
		// Portal can exist before we have a discovery row; return minimal info.
		name := "Chat"
		return &bridgev2.ChatInfo{Name: &name}, nil
	}
	name := row.Name
	var roomType database.RoomType
	if row.IsOneToOne {
		roomType = database.RoomTypeDM
	} else {
		roomType = database.RoomTypeDefault
	}
	info := &bridgev2.ChatInfo{
		Name:        &name,
		Type:        &roomType,
		CanBackfill: true,
	}

	// Fetch fresh conversation data for topic and members.
	if err := c.ensureValidSkypeToken(ctx); err == nil {
		convs, convErr := c.getAPI().ListConversations(ctx, c.skypeToken())
		if convErr == nil {
			for _, conv := range convs {
				thread, ok := conv.NormalizeForSelf(c.Meta.TeamsUserID)
				if ok && thread.ID == threadID {
					if topic := conv.ResolveTopic(); topic != "" {
						info.Topic = &topic
					}
					if members := c.buildChatMemberList(conv, row.IsOneToOne); members != nil {
						info.Members = members
					}
					break
				}
			}
		}
	}
	// Enterprise DMs come without member data; both members are in the ID.
	if info.Members == nil && row.IsOneToOne {
		info.Members = c.dmMemberListFromThreadID(threadID)
	}
	return info, nil
}

func (c *TeamsClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if c == nil || c.Main == nil || c.Main.DB == nil || ghost == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	info := &bridgev2.UserInfo{}
	if string(ghost.ID) == systemSenderID {
		// The sender of system notices is the bridge's own, not a person.
		name := systemSenderName
		isBot := true
		info.Name = &name
		info.IsBot = &isBot
		return info, nil
	}
	// Profile table first, then Graph; a ghost first seen through a reaction
	// or a meeting has no message to learn its name from.
	if name, _ := c.lookupUserDisplayName(ctx, string(ghost.ID)); name != "" {
		info.Name = &name
	} else {
		info.Name = ptrString(string(ghost.ID))
	}

	// Try to fetch avatar from Graph API.
	avatar := c.fetchUserAvatar(ctx, string(ghost.ID))
	if avatar != nil {
		info.Avatar = avatar
	}
	return info, nil
}

func (c *TeamsClient) fetchUserAvatar(ctx context.Context, teamsUserID string) *bridgev2.Avatar {
	if c == nil || c.Meta == nil {
		return nil
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
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

	userID := teamsUserID
	return &bridgev2.Avatar{
		ID: networkid.AvatarID("graph-photo:" + userID),
		Get: func(ctx context.Context) ([]byte, error) {
			data, _, err := gc.GetUserPhoto(ctx, userID)
			if err != nil {
				return nil, err
			}
			return data, nil
		},
	}
}

func (c *TeamsClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	_ = ctx
	_ = portal
	fileFeatures := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"*/*": event.CapLevelFullySupported,
		},
		Caption: event.CapLevelFullySupported,
		MaxSize: internalbridge.MaxAttachmentBytesV0,
	}
	return &event.RoomFeatures{
		// Bump when capabilities change so Beeper refreshes cached feature info.
		ID: "fi.mau.teams.capabilities.2026_09_15_1",
		File: event.FileFeatureMap{
			event.MsgFile:  fileFeatures,
			event.MsgImage: fileFeatures,
			event.MsgVideo: fileFeatures,
			event.MsgAudio: fileFeatures,
		},
		Reaction:               event.CapLevelFullySupported,
		Reply:                  event.CapLevelFullySupported,
		Thread:                 event.CapLevelFullySupported,
		Edit:                   event.CapLevelFullySupported,
		Delete:                 event.CapLevelFullySupported,
		TypingNotifications:    true,
		ReadReceipts:           true,
		PerMessageProfileRelay: true,
	}
}

func (c *TeamsClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	log := c.log()
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}
	if params.Portal == nil {
		return nil, errors.New("missing portal")
	}

	threadID := strings.TrimSpace(string(params.Portal.ID))
	if threadID == "" {
		return nil, errors.New("missing thread id")
	}

	// Resolve the conversation ID from thread state.
	row, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, threadID)
	if err != nil || row == nil {
		return nil, fmt.Errorf("no thread state for %s", threadID)
	}

	count := params.Count
	if count <= 0 {
		count = 50
	}

	// Use anchor message timestamp for pagination.
	var startTime string
	if params.AnchorMessage != nil && !params.Forward {
		startTime = params.AnchorMessage.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	log.Debug().Str("thread_id", threadID).Int("count", count).Bool("forward", params.Forward).Msg("Fetching messages for backfill")

	msgs, err := c.getAPI().ListMessagesPaginated(ctx, row.Conversation, count, startTime)
	if err != nil {
		log.Warn().Err(err).Str("thread_id", threadID).Msg("Failed to fetch messages for backfill")
		return nil, err
	}

	selfID := ""
	if c.Meta != nil {
		selfID = model.NormalizeTeamsUserID(c.Meta.TeamsUserID)
	}

	backfillMsgs := make([]*bridgev2.BackfillMessage, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.MessageID) == "" {
			continue
		}
		senderID := model.NormalizeTeamsUserID(msg.SenderID)
		system := senderID == "" || isLikelyThreadID(senderID) || isSystemMessageType(msg.MessageType)
		if system {
			if _, ok := parseSystemMessage(msg.MessageType, msg.RawContent); !ok {
				continue
			}
		}
		if strings.Contains(msg.MessageType, "MessageDelete") {
			continue
		}

		es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(senderID)}
		if system {
			es = systemEventSender()
		} else if selfID != "" && senderID == selfID {
			es.IsFromMe = true
			es.SenderLogin = c.Login.ID
		}

		// Use the bot intent for media uploads during backfill.
		var intent bridgev2.MatrixAPI
		if c.Main != nil && c.Main.Bridge != nil {
			intent = c.Main.Bridge.Bot
		}
		convert := c.convertTeamsMessage
		if system {
			convert = c.convertSystemMessage
		}
		converted, convErr := convert(ctx, params.Portal, intent, msg)
		if convErr != nil || converted == nil {
			log.Debug().Str("message_id", msg.MessageID).Str("message_type", msg.MessageType).Err(convErr).Msg("Skipping unconvertible backfill message")
			continue
		}

		backfillMsgs = append(backfillMsgs, &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           es,
			ID:               networkid.MessageID(msg.MessageID),
			Timestamp:        msg.Timestamp,
			StreamOrder:      msg.Timestamp.UnixMilli(),
		})
	}

	log.Debug().Str("thread_id", threadID).Int("fetched", len(msgs)).Int("converted", len(backfillMsgs)).Msg("Backfill complete")

	return &bridgev2.FetchMessagesResponse{
		Messages: backfillMsgs,
		HasMore:  len(backfillMsgs) >= count,
	}, nil
}

func (c *TeamsClient) ConnectBackground(ctx context.Context, _ *bridgev2.ConnectBackgroundParams) error {
	// For now, background sync just runs one discovery+poll cycle and returns.
	if c == nil {
		return nil
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return err
	}
	return c.syncOnce(ctx)
}

func (c *TeamsClient) getConsumerHTTP() *http.Client {
	if c == nil {
		return nil
	}
	c.consumerHTTPMu.Lock()
	defer c.consumerHTTPMu.Unlock()
	if c.consumerHTTP != nil {
		return c.consumerHTTP
	}
	authClient := auth.NewClient(nil)
	c.consumerHTTP = authClient.HTTP
	return c.consumerHTTP
}

func (c *TeamsClient) newConsumer() *consumerclient.Client {
	if c == nil {
		return nil
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil
	}
	consumer := consumerclient.NewClient(httpClient)
	if c.Login != nil {
		consumer.Log = &c.Login.Log
	}
	c.applyTokenToConsumer(consumer)
	return consumer
}

// applyTokenToConsumer copies the current Skype token and region URLs onto a
// consumer client. It is called when the client is built and after every
// token refresh, so the cached API client never keeps a stale token.
func (c *TeamsClient) applyTokenToConsumer(consumer *consumerclient.Client) {
	if c == nil || consumer == nil {
		return
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.applyTokenToConsumerLocked(consumer)
}

func (c *TeamsClient) applyTokenToConsumerLocked(consumer *consumerclient.Client) {
	if c.Meta == nil || consumer == nil {
		return
	}
	consumer.Token = c.Meta.SkypeToken
	// Override consumer API URLs with enterprise region-specific URLs
	// when available from the skypetoken regionGtms response.
	if chatSvc := strings.TrimSpace(c.Meta.RegionChatServiceURL); chatSvc != "" {
		chatSvc = strings.TrimRight(chatSvc, "/")
		consumer.ConversationsURL = chatSvc + "/v1/users/ME/conversations"
		consumer.MessagesURL = chatSvc + "/v1/users/ME/conversations"
		consumer.SendMessagesURL = chatSvc + "/v1/users/ME/conversations"
		consumer.ConsumptionHorizonsURL = chatSvc + "/v1/threads"
	}
}

func (c *TeamsClient) getAPI() TeamsAPI {
	c.apiMu.RLock()
	api := c.api
	c.apiMu.RUnlock()
	if api != nil {
		return api
	}
	return c.newConsumer()
}

// refreshCachedConsumerToken pushes the latest Skype token into the cached
// consumer client, if that is what the cached API is. Must be called with
// tokenMu held.
func (c *TeamsClient) refreshCachedConsumerTokenLocked() {
	c.apiMu.RLock()
	api := c.api
	c.apiMu.RUnlock()
	if consumer, ok := api.(*consumerclient.Client); ok {
		c.applyTokenToConsumerLocked(consumer)
	}
}

func (c *TeamsClient) recordSelfMessage(clientMessageID string) {
	clientMessageID = strings.TrimSpace(clientMessageID)
	if clientMessageID == "" {
		return
	}
	c.selfMessageMu.Lock()
	defer c.selfMessageMu.Unlock()
	now := time.Now().UTC()
	if c.selfMessages == nil {
		c.selfMessages = make(map[string]time.Time)
	}
	c.cleanupSelfMessagesLocked(now)
	c.selfMessages[clientMessageID] = now
}

func (c *TeamsClient) consumeSelfMessage(clientMessageID string) bool {
	clientMessageID = strings.TrimSpace(clientMessageID)
	if clientMessageID == "" {
		return false
	}
	c.selfMessageMu.Lock()
	defer c.selfMessageMu.Unlock()
	if c.selfMessages == nil {
		return false
	}
	now := time.Now().UTC()
	c.cleanupSelfMessagesLocked(now)
	_, exists := c.selfMessages[clientMessageID]
	if exists {
		delete(c.selfMessages, clientMessageID)
	}
	return exists
}

func (c *TeamsClient) cleanupSelfMessagesLocked(now time.Time) {
	if c.selfMessages == nil {
		return
	}
	for id, ts := range c.selfMessages {
		if now.Sub(ts) > selfMessageTTL {
			delete(c.selfMessages, id)
		}
	}
}

// log returns the connector's leveled logger, enriched with the current login ID.
func (c *TeamsClient) log() zerolog.Logger {
	if c == nil || c.Main == nil {
		return zerolog.Nop()
	}
	l := c.Main.Log
	if c.Login != nil {
		l = l.With().Str("login_id", string(c.Login.ID)).Logger()
	}
	return l
}

// teamSpaceChatInfo describes a team's space portal: the team's display
// name from Graph, else the name the portal already has, else "Team".
func (c *TeamsClient) teamSpaceChatInfo(ctx context.Context, teamID string, portal *bridgev2.Portal) *bridgev2.ChatInfo {
	spaceType := database.RoomTypeSpace
	// A stored "Chat" is the generic fallback, not a real team name; ignore it.
	name := ""
	if portal != nil {
		if pn := strings.TrimSpace(portal.Name); pn != "" && pn != "Chat" {
			name = pn
		}
	}
	// Prefer a name resolved during discovery (covers teams me/joinedTeams omits).
	if cached := c.cachedTeamName(teamID); cached != "" {
		name = cached
	} else if gc, err := c.getGraphClient(ctx); err == nil {
		if teams, err := gc.ListJoinedTeams(ctx); err == nil {
			for _, team := range teams {
				if strings.TrimSpace(team.ID) == teamID && strings.TrimSpace(team.DisplayName) != "" {
					name = strings.TrimSpace(team.DisplayName)
					break
				}
			}
		}
	}
	if name == "" {
		name = "Team"
	}
	return &bridgev2.ChatInfo{Name: &name, Type: &spaceType}
}

// remoteDisplayName is the name shown for this login (personal space name,
// login listings): the user's Teams display name when known, else the ID.
func (c *TeamsClient) remoteDisplayName(ctx context.Context) string {
	id := ""
	if c.Meta != nil {
		id = c.Meta.TeamsUserID
	}
	if c.Main != nil && c.Main.DB != nil && id != "" {
		if profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, id); err == nil && profile != nil && strings.TrimSpace(profile.DisplayName) != "" {
			return strings.TrimSpace(profile.DisplayName)
		}
	}
	return id
}

// saveLogin persists login metadata. It is a no-op when the login is not
// attached to a bridge (unit tests), where UserLogin.Save would dereference nil.
func (c *TeamsClient) saveLogin(ctx context.Context) error {
	if c == nil || c.Login == nil || c.Login.Bridge == nil {
		return nil
	}
	if c.superseded() {
		// The login's metadata is the new client's now; these tokens are old.
		log := c.log()
		log.Debug().Msg("Not saving login metadata from a superseded client")
		return nil
	}
	return c.Login.Save(ctx)
}

func ptrString(v string) *string { return &v }
