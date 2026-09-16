package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
)

func TestDeviceCodeLoginStartShowsCode(t *testing.T) {
	var gotClientID, gotScope, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath = r.URL.Path
		gotClientID = r.Form.Get("client_id")
		gotScope = r.Form.Get("scope")
		_, _ = w.Write([]byte(`{"device_code":"dev","user_code":"WXYZ-9876","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":5}`))
	}))
	defer server.Close()

	origFactory := newAuthClient
	newAuthClient = func(store *auth.CookieStore) *auth.Client {
		client := auth.NewClient(store)
		client.HTTP = server.Client()
		return client
	}
	defer func() { newAuthClient = origFactory }()

	main := &TeamsConnector{}
	main.Config.TokenEndpoint = server.URL + "/tenant/oauth2/v2.0/token"
	login := &DeviceCodeLogin{Main: main, User: &bridgev2.User{}}

	step, err := login.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if gotPath != "/tenant/oauth2/v2.0/devicecode" {
		t.Fatalf("device code request hit %q", gotPath)
	}
	if gotClientID != DefaultDeviceCodeClientID {
		t.Fatalf("unexpected client id %q", gotClientID)
	}
	if gotScope != DefaultDeviceCodeScope {
		t.Fatalf("unexpected scope %q", gotScope)
	}
	if step.Type != bridgev2.LoginStepTypeDisplayAndWait || step.StepID != LoginStepIDDeviceCode {
		t.Fatalf("unexpected step: %+v", step)
	}
	if step.DisplayAndWaitParams == nil || step.DisplayAndWaitParams.Type != bridgev2.LoginDisplayTypeCode || step.DisplayAndWaitParams.Data != "WXYZ-9876" {
		t.Fatalf("unexpected display params: %+v", step.DisplayAndWaitParams)
	}
	if !strings.Contains(step.Instructions, "https://microsoft.com/devicelogin") || !strings.Contains(step.Instructions, "15 minutes") {
		t.Fatalf("unexpected instructions: %q", step.Instructions)
	}
	if strings.Contains(step.Instructions, "dev") && strings.Contains(step.Instructions, "device_code") {
		t.Fatalf("instructions must not leak the device code")
	}
}

func TestDeviceCodeLoginWaitBeforeStart(t *testing.T) {
	login := &DeviceCodeLogin{Main: &TeamsConnector{}, User: &bridgev2.User{}}
	if _, err := login.Wait(context.Background()); err == nil {
		t.Fatalf("Wait before Start should fail")
	}
}

func TestBuildDeviceCodeLoginMetadata(t *testing.T) {
	var graphScope string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt-1" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		graphScope = r.Form.Get("scope")
		_, _ = w.Write([]byte(`{"access_token":"graph-at","refresh_token":"rt-2","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	skypeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("unexpected authorization header %q", got)
		}
		_, _ = w.Write([]byte(`{"tokens":{"skypeToken":"hdr.eyJza3lwZWlkIjogIm9yZ2lkOmFiYyJ9.sig","expiresIn":86400},"regionGtms":{"chatService":"https://amer.ng.msg.teams.microsoft.com","ams":"https://us-api.asm.skype.com"}}`))
	}))
	defer skypeServer.Close()

	client := auth.NewClient(nil)
	client.HTTP = tokenServer.Client()
	client.ClientID = "public-client"
	client.TokenEndpoint = tokenServer.URL + "/tenant/oauth2/v2.0/token"
	client.SkypeTokenEndpoint = skypeServer.URL
	client.Scopes = strings.Fields(DefaultDeviceCodeScope)

	state := &auth.AuthState{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}
	meta, err := buildDeviceCodeLoginMetadata(context.Background(), client, state, &TeamsConnector{})
	if err != nil {
		t.Fatalf("buildDeviceCodeLoginMetadata failed: %v", err)
	}
	if meta.LoginMethod != LoginMethodDeviceCode || meta.ClientID != "public-client" {
		t.Fatalf("unexpected login method/client: %q %q", meta.LoginMethod, meta.ClientID)
	}
	if meta.RefreshScope != DefaultDeviceCodeScope {
		t.Fatalf("unexpected refresh scope %q", meta.RefreshScope)
	}
	if meta.TokenEndpoint != client.TokenEndpoint || meta.SkypeTokenEndpoint != skypeServer.URL {
		t.Fatalf("endpoints not persisted per login: %+v", meta)
	}
	if meta.SkypeToken == "" || meta.SkypeTokenExpiresAt == 0 {
		t.Fatalf("skype token not stored: %+v", meta)
	}
	if meta.TeamsUserID != "8:orgid:abc" {
		t.Fatalf("teams user id should come from the skypetoken JWT, got %q", meta.TeamsUserID)
	}
	if meta.RegionChatServiceURL != "https://amer.ng.msg.teams.microsoft.com" || meta.RegionAmsURL != "https://us-api.asm.skype.com" {
		t.Fatalf("region URLs not stored: %+v", meta)
	}
	if graphScope != DefaultDeviceCodeGraphScope {
		t.Fatalf("graph refresh used scope %q", graphScope)
	}
	if meta.GraphAccessToken != "graph-at" || meta.GraphExpiresAt == 0 {
		t.Fatalf("graph token not stored: %+v", meta)
	}
	if meta.RefreshToken != "rt-2" {
		t.Fatalf("rotated refresh token from the graph refresh should be kept, got %q", meta.RefreshToken)
	}
}

func TestBuildDeviceCodeLoginMetadataGraphFailureIsSoft(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_scope"}`))
	}))
	defer tokenServer.Close()
	skypeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"skypeToken":{"skypetoken":"skype-1","expiresIn":86400,"skypeid":"orgid:abc"}}`))
	}))
	defer skypeServer.Close()

	client := auth.NewClient(nil)
	client.HTTP = tokenServer.Client()
	client.ClientID = "public-client"
	client.TokenEndpoint = tokenServer.URL
	client.SkypeTokenEndpoint = skypeServer.URL

	state := &auth.AuthState{AccessToken: "at-1", RefreshToken: "rt-1"}
	meta, err := buildDeviceCodeLoginMetadata(context.Background(), client, state, nil)
	if err != nil {
		t.Fatalf("graph failure must not fail login: %v", err)
	}
	if meta.GraphAccessToken != "" || meta.RefreshToken != "rt-1" || meta.TeamsUserID != "8:orgid:abc" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	if _, err := buildDeviceCodeLoginMetadata(context.Background(), client, &auth.AuthState{AccessToken: "at"}, nil); err == nil {
		t.Fatalf("missing refresh token should fail login")
	}
}
