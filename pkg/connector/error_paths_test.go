package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

// ---------------------------------------------------------------------------
// Mock that returns errors
// ---------------------------------------------------------------------------

type errorMockTeamsAPI struct {
	mockTeamsAPI
	sendErr         error
	editErr         error
	deleteErr       error
	reactionErr     error
	typingErr       error
	horizonErr      error
	topicErr        error
	addMemberErr    error
	removeMemberErr error
}

func (m *errorMockTeamsAPI) SendMessageWithID(_ context.Context, _, _, _, _ string) (int, error) {
	return 0, m.sendErr
}

func (m *errorMockTeamsAPI) SendMessageWithMentions(_ context.Context, _, _, _, _ string, _ []map[string]any) (int, error) {
	return 0, m.sendErr
}

func (m *errorMockTeamsAPI) EditMessage(_ context.Context, _, _, _, _ string) error {
	return m.editErr
}

func (m *errorMockTeamsAPI) DeleteMessage(_ context.Context, _, _, _ string) error {
	return m.deleteErr
}

func (m *errorMockTeamsAPI) AddReaction(_ context.Context, _, _, _ string, _ int64) (int, error) {
	return 0, m.reactionErr
}

func (m *errorMockTeamsAPI) SendTypingIndicator(_ context.Context, _, _ string) (int, error) {
	return 0, m.typingErr
}

func (m *errorMockTeamsAPI) UpdateConversationTopic(_ context.Context, _, _ string) error {
	return m.topicErr
}

func (m *errorMockTeamsAPI) AddMember(_ context.Context, _, _ string) error {
	return m.addMemberErr
}

func (m *errorMockTeamsAPI) RemoveMember(_ context.Context, _, _ string) error {
	return m.removeMemberErr
}

// newErrorTestClient creates a client with an error-returning mock API.
func newErrorTestClient(api TeamsAPI) *TeamsClient {
	c := &TeamsClient{
		Main: &TeamsConnector{},
		Meta: &teamsid.UserLoginMetadata{
			SkypeToken:          "test-token",
			SkypeTokenExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
			TeamsUserID:         testSelfUserID,
		},
		api: api,
	}
	c.loggedIn.Store(true)
	c.Login = &bridgev2.UserLogin{
		UserLogin: &database.UserLogin{ID: testLoginID},
	}
	return c
}

// ---------------------------------------------------------------------------
// HandleMatrixMessage error paths
// ---------------------------------------------------------------------------

func TestHandleMatrixMessage_SendError(t *testing.T) {
	api := &errorMockTeamsAPI{sendErr: errors.New("network timeout")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  portal,
			Event:   newTestEvent(),
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "hello"},
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from send failure")
	}
}

func TestHandleMatrixMessage_NilContent(t *testing.T) {
	c := newErrorTestClient(&mockTeamsAPI{})

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal: portal,
			Event:  newTestEvent(),
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for nil content")
	}
}

func TestHandleMatrixMessage_EmptyPortalID(t *testing.T) {
	c := newErrorTestClient(&mockTeamsAPI{})

	portal := newTestPortal(networkid.PortalID(""), "!room:test")
	msg := &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  portal,
			Event:   newTestEvent(),
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "hello"},
		},
	}

	_, err := c.HandleMatrixMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for empty portal ID")
	}
}

// ---------------------------------------------------------------------------
// HandleMatrixEdit error paths
// ---------------------------------------------------------------------------

func TestHandleMatrixEdit_EditError(t *testing.T) {
	api := &errorMockTeamsAPI{editErr: errors.New("edit forbidden")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixEdit{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  portal,
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "edited"},
		},
		EditTarget: &database.Message{ID: networkid.MessageID("target-123")},
	}

	err := c.HandleMatrixEdit(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from edit failure")
	}
	if !errors.Is(err, api.editErr) {
		t.Errorf("expected wrapped edit error, got %v", err)
	}
}

func TestHandleMatrixEdit_NilTarget(t *testing.T) {
	c := newErrorTestClient(&mockTeamsAPI{})

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixEdit{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  portal,
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "edited"},
		},
	}

	err := c.HandleMatrixEdit(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for nil target")
	}
}

func TestHandleMatrixEdit_EmptyThreadID(t *testing.T) {
	c := newErrorTestClient(&mockTeamsAPI{})

	portal := newTestPortal(networkid.PortalID(""), "!room:test")
	msg := &bridgev2.MatrixEdit{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Portal:  portal,
			Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "edited"},
		},
		EditTarget: &database.Message{ID: networkid.MessageID("target-123")},
	}

	err := c.HandleMatrixEdit(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for empty thread ID")
	}
}

// ---------------------------------------------------------------------------
// HandleMatrixMessageRemove error paths
// ---------------------------------------------------------------------------

func TestHandleMatrixMessageRemove_DeleteError(t *testing.T) {
	api := &errorMockTeamsAPI{deleteErr: errors.New("not found")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixMessageRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: portal,
		},
		TargetMessage: &database.Message{ID: networkid.MessageID("del-target")},
	}

	err := c.HandleMatrixMessageRemove(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from delete failure")
	}
}

func TestHandleMatrixMessageRemove_NilTarget(t *testing.T) {
	c := newErrorTestClient(&mockTeamsAPI{})

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixMessageRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: portal,
		},
	}

	err := c.HandleMatrixMessageRemove(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for nil target")
	}
}

// ---------------------------------------------------------------------------
// HandleMatrixRoomName error paths
// ---------------------------------------------------------------------------

func TestHandleMatrixRoomName_TopicUpdateError(t *testing.T) {
	api := &errorMockTeamsAPI{topicErr: errors.New("permission denied")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Portal:  portal,
			Content: &event.RoomNameEventContent{Name: "New Name"},
		},
	}

	ok, err := c.HandleMatrixRoomName(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from topic update failure")
	}
	if ok {
		t.Error("expected false on error")
	}
}

func TestHandleMatrixRoomTopic_Error(t *testing.T) {
	api := &errorMockTeamsAPI{topicErr: errors.New("forbidden")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	msg := &bridgev2.MatrixRoomTopic{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.TopicEventContent]{
			Portal: portal,
			Content: &event.TopicEventContent{Topic: "New Topic"},
		},
	}

	ok, err := c.HandleMatrixRoomTopic(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Error("expected false on error")
	}
}

// ---------------------------------------------------------------------------
// HandleMatrixMembership error paths
// ---------------------------------------------------------------------------

func TestHandleMatrixMembership_AddMemberError(t *testing.T) {
	api := &errorMockTeamsAPI{addMemberErr: errors.New("rate limited")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	ghost := &bridgev2.Ghost{
		Ghost: &database.Ghost{ID: networkid.UserID("8:orgid:target-user")},
	}
	msg := &bridgev2.MatrixMembershipChange{
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Portal: portal,
			},
		},
		Type:   bridgev2.Invite,
		Target: ghost,
	}

	_, err := c.HandleMatrixMembership(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from add member failure")
	}
}

func TestHandleMatrixMembership_RemoveMemberError(t *testing.T) {
	api := &errorMockTeamsAPI{removeMemberErr: errors.New("not allowed")}
	c := newErrorTestClient(api)

	portal := newTestPortal(networkid.PortalID(testThreadID), "!room:test")
	ghost := &bridgev2.Ghost{
		Ghost: &database.Ghost{ID: networkid.UserID("8:orgid:target-user")},
	}
	msg := &bridgev2.MatrixMembershipChange{
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{
				Portal: portal,
			},
		},
		Type:   bridgev2.Kick,
		Target: ghost,
	}

	_, err := c.HandleMatrixMembership(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error from remove member failure")
	}
}

// ---------------------------------------------------------------------------
// pollThread error paths
// ---------------------------------------------------------------------------

func TestPollThread_ListMessagesError(t *testing.T) {
	api := &mockTeamsAPI{
		listMessagesErr: errors.New("API unavailable"),
	}
	sink := &capturingEventSink{}
	c := newTestClient(api, sink)
	th := newTestThreadState()

	ingested, err := c.pollThread(context.Background(), th, time.Now())
	if err == nil {
		t.Fatal("expected error from ListMessages failure")
	}
	if ingested != 0 {
		t.Errorf("expected 0 ingested on error, got %d", ingested)
	}
	if len(sink.getEvents()) != 0 {
		t.Error("expected no events on error")
	}
}

func TestPollThread_NilClient(t *testing.T) {
	var c *TeamsClient
	ingested, err := c.pollThread(context.Background(), newTestThreadState(), time.Now())
	if err != nil {
		t.Errorf("nil client should return no error, got %v", err)
	}
	if ingested != 0 {
		t.Errorf("nil client should return 0, got %d", ingested)
	}
}

func TestPollThread_NilThreadState(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	ingested, err := c.pollThread(context.Background(), nil, time.Now())
	if err != nil {
		t.Errorf("nil thread state should return no error, got %v", err)
	}
	if ingested != 0 {
		t.Errorf("nil thread state should return 0, got %d", ingested)
	}
}

// ---------------------------------------------------------------------------
// wrapTeamsSendError
// ---------------------------------------------------------------------------

func TestWrapTeamsSendError_GenericPassthrough(t *testing.T) {
	err := errors.New("something failed")
	result := wrapTeamsSendError(err)
	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error() != err.Error() {
		t.Errorf("expected passthrough, got %q vs %q", result.Error(), err.Error())
	}
}

// ---------------------------------------------------------------------------
// convertTeamsEdit error paths
// ---------------------------------------------------------------------------

func TestConvertTeamsEdit_NilExistingParts(t *testing.T) {
	c := &TeamsClient{}
	_, err := c.convertTeamsEdit(context.Background(), nil, nil, nil, model.RemoteMessage{Body: "x"})
	if err == nil {
		t.Fatal("expected error for nil existing parts")
	}
}

func TestConvertTeamsEdit_EmptyExistingParts(t *testing.T) {
	c := &TeamsClient{}
	_, err := c.convertTeamsEdit(context.Background(), nil, nil, []*database.Message{}, model.RemoteMessage{Body: "x"})
	if err == nil {
		t.Fatal("expected error for empty existing parts")
	}
}

// ---------------------------------------------------------------------------
// IsLoggedIn edge cases
// ---------------------------------------------------------------------------

func TestIsLoggedIn_NilClient(t *testing.T) {
	var c *TeamsClient
	if c.IsLoggedIn() {
		t.Error("nil client should not be logged in")
	}
}

func TestIsLoggedIn_ExpiredToken(t *testing.T) {
	c := &TeamsClient{
		Meta: &teamsid.UserLoginMetadata{
			SkypeToken:          "token",
			SkypeTokenExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
		},
		Login: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: testLoginID},
		},
	}
	if c.IsLoggedIn() {
		t.Error("expired token should not be logged in")
	}
}

func TestIsLoggedIn_ValidToken(t *testing.T) {
	c := &TeamsClient{
		Meta: &teamsid.UserLoginMetadata{
			SkypeToken:          "token",
			SkypeTokenExpiresAt: time.Now().Add(1 * time.Hour).Unix(),
		},
		Login: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: testLoginID},
		},
	}
	if !c.IsLoggedIn() {
		t.Error("valid token should be logged in")
	}
}
