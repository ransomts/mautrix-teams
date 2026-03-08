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

	api    TeamsAPI
	events EventSink

	consumerHTTPMu sync.Mutex
	consumerHTTP   *http.Client

	syncMu     sync.Mutex
	syncCancel context.CancelFunc
	syncDone   chan struct{}

	reactionSeenMu sync.Mutex
	reactionSeen   map[string]struct{}

	receiptPollMu sync.Mutex
	receiptPoll   map[string]time.Time
	unreadMu      sync.Mutex
	unreadSeen    map[string]bool
	unreadSent    map[string]bool
	selfMessageMu sync.Mutex
	selfMessages  map[string]time.Time

	presenceMu    sync.Mutex
	presenceCache map[string]string // userID -> last known availability

	knownUsersMu sync.Mutex
	knownUsers   map[string]struct{}

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
		c.Login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Message:    err.Error(),
			UserAction: status.UserActionRelogin,
		})
		return
	}

	c.loggedIn.Store(true)
	c.api = c.newConsumer()
	c.events = &loginEventSink{login: c.Login}
	log.Info().Str("teams_user_id", c.Meta.TeamsUserID).Msg("Connected to Teams")
	c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
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
	if c.Meta == nil || c.Meta.SkypeToken == "" || c.Meta.SkypeTokenExpiresAt == 0 {
		return false
	}
	expiresAt := time.Unix(c.Meta.SkypeTokenExpiresAt, 0).UTC()
	return time.Now().UTC().Add(auth.SkypeTokenExpirySkew).Before(expiresAt)
}

func (c *TeamsClient) LogoutRemote(ctx context.Context) {
	if c == nil || c.Login == nil {
		return
	}
	c.stopSyncLoop(5 * time.Second)
	if meta, ok := c.Login.Metadata.(*teamsid.UserLoginMetadata); ok && meta != nil {
		*meta = teamsid.UserLoginMetadata{}
	}
	_ = c.Login.Save(ctx)
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
		convs, convErr := c.getAPI().ListConversations(ctx, c.Meta.SkypeToken)
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
	return info, nil
}

func (c *TeamsClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if c == nil || c.Main == nil || c.Main.DB == nil || ghost == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, string(ghost.ID))
	if err != nil {
		return nil, err
	}
	info := &bridgev2.UserInfo{}
	if profile != nil && strings.TrimSpace(profile.DisplayName) != "" {
		info.Name = &profile.DisplayName
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
		ID: "fi.mau.teams.capabilities.2026_03_08_2",
		File: event.FileFeatureMap{
			event.MsgFile:  fileFeatures,
			event.MsgImage: fileFeatures,
			event.MsgVideo: fileFeatures,
			event.MsgAudio: fileFeatures,
		},
		Reaction:               event.CapLevelFullySupported,
		Reply:                  event.CapLevelFullySupported,
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
		if senderID == "" || isLikelyThreadID(senderID) {
			continue
		}
		if strings.Contains(msg.MessageType, "MessageDelete") {
			continue
		}

		es := bridgev2.EventSender{Sender: teamsUserIDToNetworkUserID(senderID)}
		if selfID != "" && senderID == selfID {
			es.IsFromMe = true
			es.SenderLogin = c.Login.ID
		}

		// Use the bot intent for media uploads during backfill.
		var intent bridgev2.MatrixAPI
		if c.Main != nil && c.Main.Bridge != nil {
			intent = c.Main.Bridge.Bot
		}
		converted, convErr := c.convertTeamsMessage(ctx, params.Portal, intent, msg)
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
	if c.Meta != nil {
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
	return consumer
}

func (c *TeamsClient) getAPI() TeamsAPI {
	if c.api != nil {
		return c.api
	}
	return c.newConsumer()
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

func ptrString(v string) *string { return &v }
