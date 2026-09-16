package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func TestReactionSignatureIsOrderIndependent(t *testing.T) {
	a := []model.MessageReaction{
		{EmotionKey: "like", Users: []model.MessageReactionUser{{MRI: "8:orgid:b", TimeMS: 2}, {MRI: "8:orgid:a", TimeMS: 1}}},
		{EmotionKey: "heart", Users: []model.MessageReactionUser{{MRI: "8:orgid:c", TimeMS: 3}}},
	}
	b := []model.MessageReaction{
		{EmotionKey: "heart", Users: []model.MessageReactionUser{{MRI: "8:orgid:c", TimeMS: 3}}},
		{EmotionKey: "like", Users: []model.MessageReactionUser{{MRI: "8:orgid:a", TimeMS: 1}, {MRI: "8:orgid:b", TimeMS: 2}}},
	}
	if reactionSignature(a) != reactionSignature(b) {
		t.Fatalf("same reactions in different order should have the same signature")
	}
	if reactionSignature(nil) != "" {
		t.Fatalf("no reactions should have an empty signature")
	}
	c := append([]model.MessageReaction{}, a...)
	c[0].Users = c[0].Users[:1]
	if reactionSignature(a) == reactionSignature(c) {
		t.Fatalf("removing a reactor should change the signature")
	}
}

func TestReactionStateChangedOnlyOnChange(t *testing.T) {
	c := &TeamsClient{}
	if !c.reactionStateChanged("m1", "like|a") {
		t.Fatalf("first sighting must count as a change")
	}
	if c.reactionStateChanged("m1", "like|a") {
		t.Fatalf("unchanged signature must not count as a change")
	}
	if !c.reactionStateChanged("m1", "") {
		t.Fatalf("reactions removed must count as a change")
	}
	if c.reactionStateChanged("m1", "") {
		t.Fatalf("still empty must not count as a change")
	}
}

func TestChatInfoChangedOnlyOnChange(t *testing.T) {
	name := "Office Hours"
	roomType := ptrRoomType(false)
	members := &bridgev2.ChatMemberList{MemberMap: map[networkid.UserID]bridgev2.ChatMember{
		"a": {}, "b": {},
	}}
	info := &bridgev2.ChatInfo{Name: &name, Type: roomType, Members: members}
	c := &TeamsClient{}
	if !c.chatInfoChanged("t1", chatInfoSignature(info)) {
		t.Fatalf("first sighting must count as a change")
	}
	if c.chatInfoChanged("t1", chatInfoSignature(info)) {
		t.Fatalf("identical info must not count as a change")
	}
	members.MemberMap["c"] = bridgev2.ChatMember{}
	if !c.chatInfoChanged("t1", chatInfoSignature(info)) {
		t.Fatalf("a new member must count as a change")
	}
	topic := "hello"
	info.Topic = &topic
	if !c.chatInfoChanged("t1", chatInfoSignature(info)) {
		t.Fatalf("a new topic must count as a change")
	}
	if chatInfoSignature(nil) != "" {
		t.Fatalf("nil info should have an empty signature")
	}
}

func TestGraphRefreshFallsBackToDefaultScope(t *testing.T) {
	var scopes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		scope := r.Form.Get("scope")
		scopes = append(scopes, scope)
		if scope != graphDefaultScope {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"AADSTS65002: Consent between first party application ... must be configured via preauthorization"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"graph-default","refresh_token":"rt2","expires_in":3600,"scope":"https://graph.microsoft.com/Files.ReadWrite https://graph.microsoft.com/User.Read"}`))
	}))
	defer server.Close()

	client := auth.NewClient(nil)
	client.TokenEndpoint = server.URL
	state, err := refreshAccessTokenForGraphScope(context.Background(), client, "rt")
	if err != nil {
		t.Fatalf("expected .default fallback to succeed: %v", err)
	}
	if state.GraphAccessToken != "graph-default" {
		t.Fatalf("graph token not persisted from .default refresh: %q", state.GraphAccessToken)
	}
	if len(scopes) != 3 || scopes[2] != graphDefaultScope {
		t.Fatalf("expected specific, fallback, then .default scope requests, got %v", scopes)
	}
	if !strings.Contains(scopes[0], "Files.ReadWrite") {
		t.Fatalf("first attempt should request the specific scopes, got %q", scopes[0])
	}
}
