package connector

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

// ---------------------------------------------------------------------------
// Mock TeamsAPI
// ---------------------------------------------------------------------------

type mockTeamsAPI struct {
	mu sync.Mutex

	// Canned responses
	messages             []model.RemoteMessage
	listMessagesErr      error
	consumptionHorizons  *model.ConsumptionHorizonsResponse
	conversations        []model.RemoteConversation

	// Call tracking
	sentMessages          []sentMessage
	sentEdits             []sentEdit
	sentDeletes           []sentDelete
	sentReactions         []sentReaction
	removedReactions      []removedReaction
	sentTyping            []sentTyping
	setHorizons           []setHorizon
	updatedTopics         []updatedTopic
	createdConversations  []createdConversation
	createdGroupConvs     []createdGroupConversation
	addedMembers          []addedMember
	removedMembers        []removedMember
}

type sentMessage struct {
	ThreadID, Text, FromUserID, ClientMessageID string
	Mentions                                    []map[string]any
}
type sentEdit struct {
	ThreadID, MessageID, NewHTML, FromUserID string
}
type sentDelete struct {
	ThreadID, MessageID, FromUserID string
}
type sentReaction struct {
	ThreadID, MessageID, EmotionKey string
	AppliedAtMS                     int64
}
type removedReaction struct {
	ThreadID, MessageID, EmotionKey string
}
type sentTyping struct {
	ThreadID, FromUserID string
}
type setHorizon struct {
	ThreadID, Horizon string
}
type updatedTopic struct {
	ThreadID, Topic string
}
type createdConversation struct {
	ParticipantMRIs []string
}
type createdGroupConversation struct {
	Topic           string
	ParticipantMRIs []string
}
type addedMember struct {
	ThreadID, MemberMRI string
}
type removedMember struct {
	ThreadID, MemberMRI string
}

func (m *mockTeamsAPI) ListConversations(_ context.Context, _ string) ([]model.RemoteConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conversations, nil
}

func (m *mockTeamsAPI) ListMessages(_ context.Context, _ string, _ string) ([]model.RemoteMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages, m.listMessagesErr
}

func (m *mockTeamsAPI) GetMessage(_ context.Context, conversationID string, messageID string) (*model.RemoteMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.messages {
		if m.messages[i].MessageID == messageID {
			return &m.messages[i], nil
		}
	}
	return nil, fmt.Errorf("message %s not found", messageID)
}

func (m *mockTeamsAPI) ListMessagesPaginated(_ context.Context, _ string, _ int, _ string) ([]model.RemoteMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages, m.listMessagesErr
}

func (m *mockTeamsAPI) SendMessageWithID(_ context.Context, threadID, text, fromUserID, clientMessageID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, sentMessage{
		ThreadID: threadID, Text: text, FromUserID: fromUserID, ClientMessageID: clientMessageID,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SendMessageWithMentions(_ context.Context, threadID, text, fromUserID, clientMessageID string, mentions []map[string]any) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, sentMessage{
		ThreadID: threadID, Text: text, FromUserID: fromUserID, ClientMessageID: clientMessageID, Mentions: mentions,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SendReplyWithID(_ context.Context, threadID, text, fromUserID, clientMessageID, replyToID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, sentMessage{
		ThreadID: threadID, Text: text, FromUserID: fromUserID, ClientMessageID: clientMessageID,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SendReplyWithMentions(_ context.Context, threadID, text, fromUserID, clientMessageID, replyToID string, mentions []map[string]any) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMessages = append(m.sentMessages, sentMessage{
		ThreadID: threadID, Text: text, FromUserID: fromUserID, ClientMessageID: clientMessageID, Mentions: mentions,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SendGIFWithID(_ context.Context, _, _, _, _, _ string) (int, error) {
	return 200, nil
}

func (m *mockTeamsAPI) SendAttachmentMessageWithID(_ context.Context, _, _, _, _, _ string) (int, error) {
	return 200, nil
}

func (m *mockTeamsAPI) EditMessage(_ context.Context, threadID, messageID, newHTML, fromUserID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentEdits = append(m.sentEdits, sentEdit{
		ThreadID: threadID, MessageID: messageID, NewHTML: newHTML, FromUserID: fromUserID,
	})
	return nil
}

func (m *mockTeamsAPI) DeleteMessage(_ context.Context, threadID, messageID, fromUserID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentDeletes = append(m.sentDeletes, sentDelete{
		ThreadID: threadID, MessageID: messageID, FromUserID: fromUserID,
	})
	return nil
}

func (m *mockTeamsAPI) AddReaction(_ context.Context, threadID, messageID, emotionKey string, appliedAtMS int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentReactions = append(m.sentReactions, sentReaction{
		ThreadID: threadID, MessageID: messageID, EmotionKey: emotionKey, AppliedAtMS: appliedAtMS,
	})
	return 200, nil
}

func (m *mockTeamsAPI) RemoveReaction(_ context.Context, threadID, messageID, emotionKey string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedReactions = append(m.removedReactions, removedReaction{
		ThreadID: threadID, MessageID: messageID, EmotionKey: emotionKey,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SendTypingIndicator(_ context.Context, threadID, fromUserID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentTyping = append(m.sentTyping, sentTyping{
		ThreadID: threadID, FromUserID: fromUserID,
	})
	return 200, nil
}

func (m *mockTeamsAPI) SetConsumptionHorizon(_ context.Context, threadID, horizon string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setHorizons = append(m.setHorizons, setHorizon{
		ThreadID: threadID, Horizon: horizon,
	})
	return 200, nil
}

func (m *mockTeamsAPI) GetConsumptionHorizons(_ context.Context, _ string) (*model.ConsumptionHorizonsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consumptionHorizons, nil
}

func (m *mockTeamsAPI) UpdateConversationTopic(_ context.Context, threadID, topic string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedTopics = append(m.updatedTopics, updatedTopic{ThreadID: threadID, Topic: topic})
	return nil
}

func (m *mockTeamsAPI) CreateConversation(_ context.Context, participantMRIs []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdConversations = append(m.createdConversations, createdConversation{ParticipantMRIs: participantMRIs})
	return "19:new-dm@thread.v2", nil
}

func (m *mockTeamsAPI) CreateGroupConversation(_ context.Context, topic string, participantMRIs []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdGroupConvs = append(m.createdGroupConvs, createdGroupConversation{Topic: topic, ParticipantMRIs: participantMRIs})
	return "19:new-group@thread.v2", nil
}

func (m *mockTeamsAPI) AddMember(_ context.Context, threadID, memberMRI string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addedMembers = append(m.addedMembers, addedMember{ThreadID: threadID, MemberMRI: memberMRI})
	return nil
}

func (m *mockTeamsAPI) RemoveMember(_ context.Context, threadID, memberMRI string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedMembers = append(m.removedMembers, removedMember{ThreadID: threadID, MemberMRI: memberMRI})
	return nil
}

// ---------------------------------------------------------------------------
// Capturing EventSink
// ---------------------------------------------------------------------------

type capturingEventSink struct {
	mu     sync.Mutex
	events []bridgev2.RemoteEvent
}

func (s *capturingEventSink) QueueRemoteEvent(evt bridgev2.RemoteEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
}

func (s *capturingEventSink) getEvents() []bridgev2.RemoteEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bridgev2.RemoteEvent, len(s.events))
	copy(out, s.events)
	return out
}

func (s *capturingEventSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const (
	testLoginID    = networkid.UserLoginID("test-login")
	testSelfUserID = "8:orgid:self-uuid"
	testThreadID   = "19:thread-abc@thread.v2"
	testConvID     = "conversation-abc"
)

func newTestClient(api *mockTeamsAPI, sink *capturingEventSink) *TeamsClient {
	c := &TeamsClient{
		Main: &TeamsConnector{
			DB: &teamsdb.Database{
				ThreadState:        &teamsdb.ThreadStateQuery{},
				Profile:            &teamsdb.ProfileQuery{},
				ConsumptionHorizon: &teamsdb.ConsumptionHorizonQuery{},
			},
		},
		Meta: &teamsid.UserLoginMetadata{
			SkypeToken:          "test-skype-token",
			SkypeTokenExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
			TeamsUserID:         testSelfUserID,
		},
		api:    api,
		events: sink,
	}
	c.loggedIn.Store(true)
	// Construct a minimal UserLogin. The framework's UserLogin embeds
	// database.UserLogin, so we allocate one and set the fields we need.
	c.Login = &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{
			ID: testLoginID,
		},
		Log: zerolog.Nop(),
	}
	return c
}

func newTestThreadState() *teamsdb.ThreadState {
	return &teamsdb.ThreadState{
		ThreadID:     testThreadID,
		Conversation: testConvID,
	}
}

func newTestPortal(portalID networkid.PortalID, roomMXID id.RoomID) *bridgev2.Portal {
	p := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{ID: portalID, Receiver: testLoginID},
			MXID:      roomMXID,
		},
	}
	initPortalOutgoingMessages(p)
	return p
}

// initPortalOutgoingMessages sets the unexported outgoingMessages map on a
// bridgev2.Portal so that AddPendingToSave/RemovePending don't panic. This
// uses reflect+unsafe because the field is private to the bridgev2 package.
func initPortalOutgoingMessages(p *bridgev2.Portal) {
	v := reflect.ValueOf(p).Elem()
	f := v.FieldByName("outgoingMessages")
	if !f.IsValid() {
		return
	}
	// Use unsafe to bypass unexported field restriction.
	ptr := unsafe.Pointer(f.UnsafeAddr())
	mapPtr := (*map[networkid.TransactionID]unsafe.Pointer)(ptr)
	*mapPtr = make(map[networkid.TransactionID]unsafe.Pointer)
}

func newTestEvent() *event.Event {
	return &event.Event{
		ID:        id.EventID("$test-event"),
		Timestamp: time.Now().UnixMilli(),
	}
}

func makeRemoteMessage(msgID, seqID, senderID, body string, ts time.Time) model.RemoteMessage {
	return model.RemoteMessage{
		MessageID:   msgID,
		SequenceID:  seqID,
		SenderID:    senderID,
		Body:        body,
		MessageType: "RichText/Html",
		Timestamp:   ts,
	}
}

// ---------------------------------------------------------------------------
// Inbound pipeline: pollThread tests
// ---------------------------------------------------------------------------

func TestPollThread_IngestsNewMessages(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			makeRemoteMessage("msg-1", "100", "8:orgid:alice-uuid", "hello", time.Now()),
			makeRemoteMessage("msg-2", "101", "8:orgid:bob-uuid", "world", time.Now()),
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 2 {
		t.Fatalf("expected 2 ingested, got %d", ingested)
	}

	events := sink.getEvents()
	// Each message produces a Message event + a ReactionSync event = 4 total
	messageEvents := filterEventsByType(events, bridgev2.RemoteEventMessage)
	if len(messageEvents) != 2 {
		t.Fatalf("expected 2 message events, got %d", len(messageEvents))
	}

	// Verify sender IDs
	for _, evt := range messageEvents {
		sender := evt.GetSender()
		if sender.Sender == "" {
			t.Error("message event has empty sender")
		}
		if sender.IsFromMe {
			t.Error("non-self message marked as IsFromMe")
		}
	}
}

func TestPollThread_SkipsAlreadySeenMessages(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			makeRemoteMessage("msg-old", "50", "8:orgid:alice-uuid", "old message", time.Now()),
			makeRemoteMessage("msg-new", "101", "8:orgid:alice-uuid", "new message", time.Now()),
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()
	th.LastSequenceID = "100" // Already seen up to sequence 100

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 1 {
		t.Fatalf("expected 1 ingested (only the new message), got %d", ingested)
	}

	messageEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventMessage)
	if len(messageEvents) != 1 {
		t.Fatalf("expected 1 message event, got %d", len(messageEvents))
	}
}

func TestPollThread_DetectsMessageDelete(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:   "msg-del-1",
				SequenceID:  "100",
				SenderID:    "8:orgid:alice-uuid",
				MessageType: "Event/MessageDelete",
				Timestamp:   time.Now(),
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 0 {
		t.Fatalf("deletes should not count as ingested, got %d", ingested)
	}

	deleteEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventMessageRemove)
	if len(deleteEvents) != 1 {
		t.Fatalf("expected 1 delete event, got %d", len(deleteEvents))
	}

	del, ok := deleteEvents[0].(*simplevent.MessageRemove)
	if !ok {
		t.Fatal("event is not MessageRemove")
	}
	if del.TargetMessage != "msg-del-1" {
		t.Fatalf("expected target message msg-del-1, got %s", del.TargetMessage)
	}
}

func TestPollThread_DetectsMessageEdit(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:    "msg-edit-v2",
				SequenceID:   "100",
				SenderID:     "8:orgid:alice-uuid",
				Body:         "updated body",
				MessageType:  "RichText/Html",
				SkypeEditedID: "msg-edit-v1",
				Timestamp:    time.Now(),
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	// Edits don't count as ingested (they're updates to existing messages)
	if ingested != 0 {
		t.Fatalf("edits should not count as ingested, got %d", ingested)
	}

	editEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventEdit)
	if len(editEvents) != 1 {
		t.Fatalf("expected 1 edit event, got %d", len(editEvents))
	}
}

func TestPollThread_IdentifiesSelfMessages(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			makeRemoteMessage("msg-self", "100", testSelfUserID, "my own message", time.Now()),
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 1 {
		t.Fatalf("expected 1 ingested, got %d", ingested)
	}

	messageEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventMessage)
	if len(messageEvents) != 1 {
		t.Fatal("expected 1 message event")
	}
	sender := messageEvents[0].GetSender()
	if !sender.IsFromMe {
		t.Error("self message should be marked IsFromMe")
	}
	if sender.SenderLogin != testLoginID {
		t.Errorf("expected SenderLogin=%s, got %s", testLoginID, sender.SenderLogin)
	}
}

func TestPollThread_SkipsNonUserSenders(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			// Thread ID as sender — should be skipped
			makeRemoteMessage("msg-system", "100", "19:abc@thread.v2", "system message", time.Now()),
			// Normal user — should pass
			makeRemoteMessage("msg-user", "101", "8:orgid:alice-uuid", "user message", time.Now()),
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 1 {
		t.Fatalf("expected 1 ingested (system sender filtered), got %d", ingested)
	}
}

func TestPollThread_EmptyResponse(t *testing.T) {
	api := &mockTeamsAPI{messages: nil}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 0 {
		t.Fatalf("expected 0 ingested, got %d", ingested)
	}
	// Only reaction/receipt sync events might be generated, no message events
	messageEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventMessage)
	if len(messageEvents) != 0 {
		t.Fatalf("expected 0 message events, got %d", len(messageEvents))
	}
}

func TestPollThread_UpdatesSequenceCursor(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			makeRemoteMessage("msg-1", "200", "8:orgid:alice-uuid", "hello", time.Now()),
			makeRemoteMessage("msg-2", "300", "8:orgid:bob-uuid", "world", time.Now()),
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	_, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	// ThreadState should be updated to the highest sequence ID
	if th.LastSequenceID != "300" {
		t.Fatalf("expected LastSequenceID=300, got %s", th.LastSequenceID)
	}
}

func TestPollThread_SelfEchoDoesNotMarkUnread(t *testing.T) {
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:       "msg-echo",
				SequenceID:      "100",
				SenderID:        testSelfUserID,
				Body:            "echoed message",
				MessageType:     "RichText/Html",
				ClientMessageID: "client-123",
				Timestamp:       time.Now(),
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	// Record the client message ID as a self-sent message
	c.recordSelfMessage("client-123")
	th := newTestThreadState()

	_, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}

	// Should not be marked unread for self-echo
	c.unreadMu.Lock()
	isUnread := c.unreadSeen[testThreadID]
	c.unreadMu.Unlock()
	if isUnread {
		t.Error("self-echo should not mark thread as unread")
	}
}

func TestPollThread_ReactionsOnOlderMessagesSynced(t *testing.T) {
	// A message with seq <= last seen, but with reactions, should still generate a ReactionSync event
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:   "msg-old",
				SequenceID:  "50",
				SenderID:    "8:orgid:alice-uuid",
				Body:        "old message",
				MessageType: "RichText/Html",
				Timestamp:   time.Now(),
				Reactions: []model.MessageReaction{
					{EmotionKey: "like", Users: []model.MessageReactionUser{{MRI: "8:orgid:bob-uuid", TimeMS: 1700000000000}}},
				},
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()
	th.LastSequenceID = "100"

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 0 {
		t.Fatalf("old messages should not be ingested, got %d", ingested)
	}

	// But a ReactionSync should still be generated
	reactionEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventReactionSync)
	if len(reactionEvents) != 1 {
		t.Fatalf("expected 1 reaction sync event for old message, got %d", len(reactionEvents))
	}
}

func TestPollThread_MixedMessageTypes(t *testing.T) {
	// Simulate a realistic poll with a normal message, an edit, and a delete
	now := time.Now()
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			makeRemoteMessage("msg-normal", "100", "8:orgid:alice-uuid", "hello", now),
			{
				MessageID:     "msg-edited",
				SequenceID:    "101",
				SenderID:      "8:orgid:bob-uuid",
				Body:          "corrected text",
				MessageType:   "RichText/Html",
				SkypeEditedID: "msg-original",
				Timestamp:     now,
			},
			{
				MessageID:   "msg-deleted",
				SequenceID:  "102",
				SenderID:    "8:orgid:charlie-uuid",
				MessageType: "Event/MessageDelete",
				Timestamp:   now,
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	// Only the normal message counts as ingested
	if ingested != 1 {
		t.Fatalf("expected 1 ingested, got %d", ingested)
	}

	events := sink.getEvents()
	if len(filterEventsByType(events, bridgev2.RemoteEventMessage)) != 1 {
		t.Error("expected 1 message event")
	}
	if len(filterEventsByType(events, bridgev2.RemoteEventEdit)) != 1 {
		t.Error("expected 1 edit event")
	}
	if len(filterEventsByType(events, bridgev2.RemoteEventMessageRemove)) != 1 {
		t.Error("expected 1 delete event")
	}
}

// ---------------------------------------------------------------------------
// Outbound pipeline: HandleMatrixMessage tests
// ---------------------------------------------------------------------------

func TestHandleMatrixMessage_SendsText(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   newTestEvent(),
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "hello from matrix",
			},
			Portal: newTestPortal(networkid.PortalID(testThreadID), "!room:example.com"),
		},
	}

	resp, err := c.HandleMatrixMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMessage failed: %v", err)
	}
	if resp == nil || resp.DB == nil {
		t.Fatal("expected non-nil response with DB")
	}
	if !resp.Pending {
		t.Error("response should be pending")
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(api.sentMessages))
	}
	sent := api.sentMessages[0]
	if sent.ThreadID != testThreadID {
		t.Errorf("expected threadID=%s, got %s", testThreadID, sent.ThreadID)
	}
	if sent.Text != "hello from matrix" {
		t.Errorf("expected text='hello from matrix', got '%s'", sent.Text)
	}
	if sent.FromUserID != testSelfUserID {
		t.Errorf("expected fromUserID=%s, got %s", testSelfUserID, sent.FromUserID)
	}
}

func TestHandleMatrixMessage_SendsHTMLWhenFormatted(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   newTestEvent(),
			Content: &event.MessageEventContent{
				MsgType:       event.MsgText,
				Body:          "bold text",
				Format:        event.FormatHTML,
				FormattedBody: "<b>bold text</b>",
			},
			Portal: newTestPortal(networkid.PortalID(testThreadID), "!room:example.com"),
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMessage failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(api.sentMessages))
	}
	// Should send the formatted HTML body, not the plain text
	if api.sentMessages[0].Text != "<b>bold text</b>" {
		t.Errorf("expected HTML body, got '%s'", api.sentMessages[0].Text)
	}
}

func TestHandleMatrixMessage_SendsReply(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   newTestEvent(),
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "reply text",
			},
			Portal: newTestPortal(networkid.PortalID(testThreadID), "!room:example.com"),
		},
		ReplyTo: &database.Message{ID: "original-msg-id"},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMessage failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(api.sentMessages))
	}
	body := api.sentMessages[0].Text
	if body == "reply text" {
		t.Error("reply should wrap body with blockquote, got plain text")
	}
	expected := `<blockquote itemtype="http://schema.skype.com/Reply" itemid="original-msg-id"></blockquote>reply text`
	if body != expected {
		t.Errorf("unexpected reply body:\n  got:  %s\n  want: %s", body, expected)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixReaction
// ---------------------------------------------------------------------------

func TestHandleMatrixReaction_AddsReaction(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
			Content: &event.ReactionEventContent{
				RelatesTo: event.RelatesTo{Key: "\U0001f44d"},
			},
			Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		},
		TargetMessage: &database.Message{ID: "target-msg-id"},
		PreHandleResp: &bridgev2.MatrixReactionPreResponse{
			SenderID: teamsUserIDToNetworkUserID(testSelfUserID),
			EmojiID:  "like",
			Emoji:    "\U0001f44d",
		},
	}

	reaction, err := c.HandleMatrixReaction(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixReaction failed: %v", err)
	}
	if string(reaction.EmojiID) != "like" {
		t.Errorf("expected emojiID=like, got %s", reaction.EmojiID)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentReactions) != 1 {
		t.Fatalf("expected 1 sent reaction, got %d", len(api.sentReactions))
	}
	if api.sentReactions[0].EmotionKey != "like" {
		t.Errorf("expected emotionKey=like, got %s", api.sentReactions[0].EmotionKey)
	}
	if api.sentReactions[0].MessageID != "target-msg-id" {
		t.Errorf("expected messageID=target-msg-id, got %s", api.sentReactions[0].MessageID)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixReactionRemove
// ---------------------------------------------------------------------------

func TestHandleMatrixReactionRemove(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixReactionRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		},
		TargetReaction: &database.Reaction{
			MessageID: "target-msg-id",
			EmojiID:   "like",
		},
	}

	err := c.HandleMatrixReactionRemove(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixReactionRemove failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.removedReactions) != 1 {
		t.Fatalf("expected 1 removed reaction, got %d", len(api.removedReactions))
	}
	if api.removedReactions[0].EmotionKey != "like" {
		t.Errorf("expected emotionKey=like, got %s", api.removedReactions[0].EmotionKey)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixEdit
// ---------------------------------------------------------------------------

func TestHandleMatrixEdit(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixEdit{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "edited text",
			},
			Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		},
		EditTarget: &database.Message{ID: "original-msg-id"},
	}

	err := c.HandleMatrixEdit(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixEdit failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentEdits) != 1 {
		t.Fatalf("expected 1 sent edit, got %d", len(api.sentEdits))
	}
	edit := api.sentEdits[0]
	if edit.MessageID != "original-msg-id" {
		t.Errorf("expected messageID=original-msg-id, got %s", edit.MessageID)
	}
	if edit.NewHTML != "edited text" {
		t.Errorf("expected newHTML='edited text', got '%s'", edit.NewHTML)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixMessageRemove (redaction)
// ---------------------------------------------------------------------------

func TestHandleMatrixMessageRemove(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixMessageRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		},
		TargetMessage: &database.Message{ID: "msg-to-delete"},
	}

	err := c.HandleMatrixMessageRemove(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMessageRemove failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentDeletes) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(api.sentDeletes))
	}
	if api.sentDeletes[0].MessageID != "msg-to-delete" {
		t.Errorf("expected messageID=msg-to-delete, got %s", api.sentDeletes[0].MessageID)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixTyping
// ---------------------------------------------------------------------------

func TestHandleMatrixTyping(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixTyping{
		Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		IsTyping: true,
	}

	err := c.HandleMatrixTyping(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixTyping failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentTyping) != 1 {
		t.Fatalf("expected 1 typing call, got %d", len(api.sentTyping))
	}
	if api.sentTyping[0].ThreadID != testThreadID {
		t.Errorf("expected threadID=%s, got %s", testThreadID, api.sentTyping[0].ThreadID)
	}
}

func TestHandleMatrixTyping_NotTypingIsNoop(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixTyping{
		Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		IsTyping: false,
	}

	err := c.HandleMatrixTyping(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixTyping failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.sentTyping) != 0 {
		t.Fatalf("expected 0 typing calls for IsTyping=false, got %d", len(api.sentTyping))
	}
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func TestHandleMatrixMessage_NotLoggedIn(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	c.loggedIn.Store(false)
	// Clear Meta so IsLoggedIn() doesn't fall through to the SkypeToken check.
	c.Meta = &teamsid.UserLoginMetadata{}

	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "test"},
			Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err != bridgev2.ErrNotLoggedIn {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestHandleMatrixMessage_UnsupportedType(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	portal := newTestPortal(networkid.PortalID(testThreadID), "")
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: "notice"},
			Portal:  portal,
			Event:   newTestEvent(),
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err == nil || err.Error() != "unsupported message type" {
		t.Fatalf("expected unsupported message type error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixRoomName
// ---------------------------------------------------------------------------

func TestHandleMatrixRoomName(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Content: &event.RoomNameEventContent{Name: "New Room Name"},
			Portal:  newTestPortal(networkid.PortalID(testThreadID), "!room:example.com"),
		},
	}

	ok, err := c.HandleMatrixRoomName(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixRoomName failed: %v", err)
	}
	if !ok {
		t.Error("expected success (true)")
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.updatedTopics) != 1 {
		t.Fatalf("expected 1 topic update, got %d", len(api.updatedTopics))
	}
	if api.updatedTopics[0].ThreadID != testThreadID {
		t.Errorf("expected threadID=%s, got %s", testThreadID, api.updatedTopics[0].ThreadID)
	}
	if api.updatedTopics[0].Topic != "New Room Name" {
		t.Errorf("expected topic='New Room Name', got '%s'", api.updatedTopics[0].Topic)
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixRoomTopic
// ---------------------------------------------------------------------------

func TestHandleMatrixRoomTopic(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	msg := &bridgev2.MatrixRoomTopic{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.TopicEventContent]{
			Content: &event.TopicEventContent{Topic: "New Topic"},
			Portal:  newTestPortal(networkid.PortalID(testThreadID), "!room:example.com"),
		},
	}

	ok, err := c.HandleMatrixRoomTopic(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixRoomTopic failed: %v", err)
	}
	if !ok {
		t.Error("expected success (true)")
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.updatedTopics) != 1 {
		t.Fatalf("expected 1 topic update, got %d", len(api.updatedTopics))
	}
	if api.updatedTopics[0].Topic != "New Topic" {
		t.Errorf("expected topic='New Topic', got '%s'", api.updatedTopics[0].Topic)
	}
}

func TestHandleMatrixRoomName_NotLoggedIn(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	c.loggedIn.Store(false)
	c.Meta = &teamsid.UserLoginMetadata{}

	msg := &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Content: &event.RoomNameEventContent{Name: "Test"},
			Portal:  newTestPortal(networkid.PortalID(testThreadID), ""),
		},
	}

	_, err := c.HandleMatrixRoomName(context.Background(), msg)
	if err != bridgev2.ErrNotLoggedIn {
		t.Fatalf("expected ErrNotLoggedIn, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Outbound: CreateGroup
// ---------------------------------------------------------------------------

func TestCreateGroup(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	params := &bridgev2.GroupCreateParams{
		Participants: []networkid.UserID{"8:orgid:alice-uuid", "8:orgid:bob-uuid"},
		Name:         &event.RoomNameEventContent{Name: "Test Group"},
	}

	resp, err := c.CreateGroup(context.Background(), params)
	if err != nil {
		t.Fatalf("CreateGroup failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if string(resp.PortalKey.ID) != "19:new-group@thread.v2" {
		t.Errorf("expected portal ID 19:new-group@thread.v2, got %s", resp.PortalKey.ID)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.createdGroupConvs) != 1 {
		t.Fatalf("expected 1 group creation, got %d", len(api.createdGroupConvs))
	}
	if api.createdGroupConvs[0].Topic != "Test Group" {
		t.Errorf("expected topic='Test Group', got '%s'", api.createdGroupConvs[0].Topic)
	}
	// Should have 3 participants: self + alice + bob
	if len(api.createdGroupConvs[0].ParticipantMRIs) != 3 {
		t.Errorf("expected 3 participants, got %d", len(api.createdGroupConvs[0].ParticipantMRIs))
	}
}

func TestCreateGroup_NoParticipants(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	params := &bridgev2.GroupCreateParams{
		Participants: nil,
	}

	_, err := c.CreateGroup(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for empty participants")
	}
}

// ---------------------------------------------------------------------------
// Inbound: Typing indicator detection
// ---------------------------------------------------------------------------

func TestPollThread_DetectsTypingIndicator(t *testing.T) {
	now := time.Now()
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:   "typing-1",
				SequenceID:  "100",
				SenderID:    "8:orgid:alice-uuid",
				MessageType: "Control/Typing",
				Timestamp:   now,
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, now)
	if err != nil {
		t.Fatalf("pollThread failed: %v", err)
	}
	if ingested != 0 {
		t.Fatalf("typing indicators should not count as ingested, got %d", ingested)
	}

	typingEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventTyping)
	if len(typingEvents) != 1 {
		t.Fatalf("expected 1 typing event, got %d", len(typingEvents))
	}
}

func TestPollThread_SkipsSelfTyping(t *testing.T) {
	now := time.Now()
	api := &mockTeamsAPI{
		messages: []model.RemoteMessage{
			{
				MessageID:   "typing-self",
				SequenceID:  "100",
				SenderID:    testSelfUserID,
				MessageType: "Control/Typing",
				Timestamp:   now,
			},
		},
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	_, _ = c.pollThread(context.Background(), th, now)

	typingEvents := filterEventsByType(sink.getEvents(), bridgev2.RemoteEventTyping)
	if len(typingEvents) != 0 {
		t.Fatalf("self typing should not emit events, got %d", len(typingEvents))
	}
}

func TestShouldEmitTyping_Deduplication(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})

	// First call should return true
	if !c.shouldEmitTyping("thread-1", "user-1") {
		t.Error("first call should return true")
	}
	// Immediate second call should return false (within 3s window)
	if c.shouldEmitTyping("thread-1", "user-1") {
		t.Error("duplicate within 3s window should return false")
	}
	// Different thread should return true
	if !c.shouldEmitTyping("thread-2", "user-1") {
		t.Error("different thread should return true")
	}
	// Different user in same thread should return true
	if !c.shouldEmitTyping("thread-1", "user-2") {
		t.Error("different user should return true")
	}
}

// ---------------------------------------------------------------------------
// Message delivery status wrapping
// ---------------------------------------------------------------------------

func TestWrapTeamsSendError_NonWrappable(t *testing.T) {
	// Regular errors should pass through unchanged
	err := errors.New("some random error")
	wrapped := wrapTeamsSendError(err)
	if wrapped != err {
		t.Errorf("non-wrappable error should pass through, got different error")
	}
}

// ---------------------------------------------------------------------------
// Outbound: HandleMatrixMembership
// ---------------------------------------------------------------------------

func TestHandleMatrixMembership_Invite(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	ghost := &bridgev2.Ghost{
		Ghost: &database.Ghost{
			ID: "8:orgid:alice-uuid",
		},
	}

	msg := &bridgev2.MatrixMembershipChange{
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
			},
		},
		Target: ghost,
		Type:   bridgev2.Invite,
	}

	_, err := c.HandleMatrixMembership(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMembership (invite) failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.addedMembers) != 1 {
		t.Fatalf("expected 1 add member call, got %d", len(api.addedMembers))
	}
	if api.addedMembers[0].ThreadID != testThreadID {
		t.Errorf("expected threadID=%s, got %s", testThreadID, api.addedMembers[0].ThreadID)
	}
	if api.addedMembers[0].MemberMRI != "8:orgid:alice-uuid" {
		t.Errorf("expected memberMRI=8:orgid:alice-uuid, got %s", api.addedMembers[0].MemberMRI)
	}
}

func TestHandleMatrixMembership_Kick(t *testing.T) {
	api := &mockTeamsAPI{}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)

	ghost := &bridgev2.Ghost{
		Ghost: &database.Ghost{
			ID: "8:orgid:bob-uuid",
		},
	}

	msg := &bridgev2.MatrixMembershipChange{
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Portal: newTestPortal(networkid.PortalID(testThreadID), ""),
			},
		},
		Target: ghost,
		Type:   bridgev2.Kick,
	}

	_, err := c.HandleMatrixMembership(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMatrixMembership (kick) failed: %v", err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.removedMembers) != 1 {
		t.Fatalf("expected 1 remove member call, got %d", len(api.removedMembers))
	}
	if api.removedMembers[0].MemberMRI != "8:orgid:bob-uuid" {
		t.Errorf("expected memberMRI=8:orgid:bob-uuid, got %s", api.removedMembers[0].MemberMRI)
	}
}

// ---------------------------------------------------------------------------
// Custom emoji reactions
// ---------------------------------------------------------------------------

func TestMapEmojiToEmotionKey_Passthrough(t *testing.T) {
	// Standard emoji should map normally.
	key, ok := MapEmojiToEmotionKey("👍")
	if !ok || key != "like" {
		t.Errorf("expected like, got %s (ok=%v)", key, ok)
	}

	// Unknown Unicode emoji should pass through.
	key, ok = MapEmojiToEmotionKey("🦄")
	if !ok {
		t.Error("expected unknown unicode emoji to pass through")
	}
	if key == "" {
		t.Error("expected non-empty key for passthrough emoji")
	}
}

func TestMapEmotionKeyToEmoji_Custom(t *testing.T) {
	// Known key.
	emoji, ok := MapEmotionKeyToEmoji("like")
	if !ok || emoji == "" {
		t.Errorf("expected emoji for 'like', got %q (ok=%v)", emoji, ok)
	}

	// Unknown key should return :shortcode: format.
	emoji, ok = MapEmotionKeyToEmoji("custom_party")
	if !ok {
		t.Error("expected custom key to return true")
	}
	if emoji != ":custom_party:" {
		t.Errorf("expected :custom_party:, got %s", emoji)
	}
}

func TestIsUnicodeEmoji(t *testing.T) {
	if !IsUnicodeEmoji("🦄") {
		t.Error("expected 🦄 to be detected as emoji")
	}
	if IsUnicodeEmoji("hello") {
		t.Error("expected 'hello' to not be emoji")
	}
	if IsUnicodeEmoji("") {
		t.Error("expected empty string to not be emoji")
	}
}

// ---------------------------------------------------------------------------
// Call/meeting event conversion
// ---------------------------------------------------------------------------

func TestConvertCallEvent(t *testing.T) {
	msg := model.RemoteMessage{
		MessageType: "Event/Call/Missed",
		SenderID:    "8:orgid:alice-uuid",
		SenderName:  "Alice",
		Timestamp:   time.Now(),
	}
	cm := convertCallOrMeetingEvent(msg)
	if cm == nil {
		t.Fatal("expected non-nil conversion for call event")
	}
	if len(cm.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(cm.Parts))
	}
	if cm.Parts[0].Content.MsgType != event.MsgNotice {
		t.Errorf("expected MsgNotice, got %s", cm.Parts[0].Content.MsgType)
	}
	if cm.Parts[0].Content.Body != "Missed call from Alice" {
		t.Errorf("unexpected body: %s", cm.Parts[0].Content.Body)
	}
}

func TestConvertCallEvent_NotACall(t *testing.T) {
	msg := model.RemoteMessage{
		MessageType: "RichText/Html",
		Body:        "hello",
	}
	cm := convertCallOrMeetingEvent(msg)
	if cm != nil {
		t.Error("expected nil for non-call message")
	}
}

func TestExtractMeetingJoinURL(t *testing.T) {
	body := `Join the meeting: https://teams.microsoft.com/l/meetup-join/123abc and discuss`
	url := extractMeetingJoinURL(body)
	if url != "https://teams.microsoft.com/l/meetup-join/123abc" {
		t.Errorf("unexpected url: %s", url)
	}

	noMeeting := "just a regular message"
	if extractMeetingJoinURL(noMeeting) != "" {
		t.Error("expected empty url for non-meeting body")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func filterEventsByType(events []bridgev2.RemoteEvent, evtType bridgev2.RemoteEventType) []bridgev2.RemoteEvent {
	var out []bridgev2.RemoteEvent
	for _, evt := range events {
		if evt.GetType() == evtType {
			out = append(out, evt)
		}
	}
	return out
}
