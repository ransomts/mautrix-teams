package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

func TestTenantTokenEndpointSelection(t *testing.T) {
	cases := map[string]string{
		"": "",
		"https://login.microsoftonline.com/common/oauth2/v2.0/token":        "",
		"https://login.microsoftonline.com/consumers/oauth2/v2.0/token":     "",
		"https://login.microsoftonline.com/0c9bf8f6-ccad/oauth2/v2.0/token": "https://login.microsoftonline.com/0c9bf8f6-ccad/oauth2/v2.0/token",
	}
	for ep, want := range cases {
		main := &TeamsConnector{Config: TeamsConfig{TokenEndpoint: ep}}
		if got := tenantTokenEndpoint(main); got != want {
			t.Errorf("endpoint %q -> %q, want %q", ep, got, want)
		}
	}
	if tenantTokenEndpoint(nil) != "" {
		t.Errorf("nil connector should yield empty")
	}
}

func TestSkypeSpacesRefreshScopeShape(t *testing.T) {
	if !strings.Contains(skypeSpacesRefreshScope, "api.spaces.skype.com") ||
		!strings.Contains(skypeSpacesRefreshScope, "offline_access") {
		t.Fatalf("unexpected refresh scope: %q", skypeSpacesRefreshScope)
	}
}

func TestEnterpriseSelfRefreshWithoutMetadataFields(t *testing.T) {
	// A login with NO RefreshScope (a plain localStorage login whose override
	// fields did not survive a save) must still refresh via the tenant Spaces
	// scope when the config has a tenant token endpoint, not the MBI/common
	// scope the tenant rejects.
	var sawScope, sawURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			sawScope = r.Form.Get("scope")
			sawURL = r.URL.Path
			_, _ = w.Write([]byte(`{"access_token":"spaces-at","refresh_token":"rt2","expires_in":3600}`))
			return
		}
		// skypetoken endpoint
		_, _ = w.Write([]byte(`{"skypeToken":{"skypetoken":"fresh-skype","expiresIn":3600,"skypeid":"live:tester"}}`))
	}))
	defer server.Close()

	origFactory := newAuthClient
	newAuthClient = func(store *auth.CookieStore) *auth.Client {
		client := auth.NewClient(store)
		client.TokenEndpoint = server.URL + "/0c9bf8f6-tenant/oauth2/v2.0/token"
		client.SkypeTokenEndpoint = server.URL + "/skypetoken"
		return client
	}
	defer func() { newAuthClient = origFactory }()

	c := &TeamsClient{
		Main: &TeamsConnector{Config: TeamsConfig{
			TokenEndpoint: server.URL + "/0c9bf8f6-tenant/oauth2/v2.0/token",
			ClientID:      "5e3ce6c0-web",
		}},
		Login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{}},
		Meta: &teamsid.UserLoginMetadata{
			RefreshToken:        "browser-rt",
			SkypeToken:          "stale",
			SkypeTokenExpiresAt: 1, // expired
			// deliberately no RefreshScope / ClientID / TokenEndpoint
		},
	}
	if err := c.ensureValidSkypeToken(context.Background()); err != nil {
		t.Fatalf("enterprise self-refresh failed: %v", err)
	}
	if !strings.Contains(sawScope, "api.spaces.skype.com") {
		t.Fatalf("refresh used scope %q, want the Spaces scope", sawScope)
	}
	if strings.Contains(sawScope, "MBI_SSL") {
		t.Fatalf("must not use the MBI scope for an enterprise tenant")
	}
	if !strings.Contains(sawURL, "0c9bf8f6-tenant") {
		t.Fatalf("refresh hit %q, want the tenant endpoint", sawURL)
	}
	if c.Meta.SkypeToken != "fresh-skype" {
		t.Fatalf("skype token not renewed: %q", c.Meta.SkypeToken)
	}
}
