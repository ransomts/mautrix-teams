package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// TeamsAPI abstracts the Teams consumer HTTP client for testability.
type TeamsAPI interface {
	ListConversations(ctx context.Context, token string) ([]model.RemoteConversation, error)
	ListMessages(ctx context.Context, conversationID string, sinceSequence string) ([]model.RemoteMessage, error)
	GetMessage(ctx context.Context, conversationID string, messageID string) (*model.RemoteMessage, error)
	ListMessagesPaginated(ctx context.Context, conversationID string, pageSize int, startTime string) ([]model.RemoteMessage, error)
	SendMessageWithID(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string) (int, error)
	SendMessageWithMentions(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, mentions []map[string]any) (int, error)
	SendReplyWithID(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, replyToID string) (int, error)
	SendReplyWithMentions(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, replyToID string, mentions []map[string]any) (int, error)
	SendGIFWithID(ctx context.Context, threadID string, gifURL string, title string, fromUserID string, clientMessageID string) (int, error)
	SendAttachmentMessageWithID(ctx context.Context, threadID string, htmlContent string, filesProperty string, fromUserID string, clientMessageID string) (int, error)
	EditMessage(ctx context.Context, threadID string, messageID string, newHTMLContent string, fromUserID string) error
	DeleteMessage(ctx context.Context, threadID string, messageID string, fromUserID string) error
	AddReaction(ctx context.Context, threadID string, teamsMessageID string, emotionKey string, appliedAtMS int64) (int, error)
	RemoveReaction(ctx context.Context, threadID string, teamsMessageID string, emotionKey string) (int, error)
	SendTypingIndicator(ctx context.Context, threadID string, fromUserID string) (int, error)
	SetConsumptionHorizon(ctx context.Context, threadID string, horizon string) (int, error)
	GetConsumptionHorizons(ctx context.Context, threadID string) (*model.ConsumptionHorizonsResponse, error)
	UpdateConversationTopic(ctx context.Context, threadID string, topic string) error
	CreateConversation(ctx context.Context, participantMRIs []string) (string, error)
	CreateGroupConversation(ctx context.Context, topic string, participantMRIs []string) (string, error)
	AddMember(ctx context.Context, threadID string, memberMRI string) error
	RemoveMember(ctx context.Context, threadID string, memberMRI string) error
}

// EventSink abstracts the bridge's remote event queue for testability.
type EventSink interface {
	QueueRemoteEvent(evt bridgev2.RemoteEvent)
}

// loginEventSink wraps a bridgev2.UserLogin as an EventSink.
type loginEventSink struct {
	login *bridgev2.UserLogin
}

func (s *loginEventSink) QueueRemoteEvent(evt bridgev2.RemoteEvent) {
	s.login.QueueRemoteEvent(evt)
}
