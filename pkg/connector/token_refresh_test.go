package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

func TestRefreshFailureBackoff(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	var f refreshFailure
	if err := f.blocked(now); err != nil {
		t.Fatalf("fresh state should not block: %v", err)
	}

	f.record(now, errors.New("dial tcp: network unreachable"))
	if err := f.blocked(now.Add(refreshBackoffBase - time.Second)); err == nil {
		t.Fatalf("expected block inside first backoff window")
	}
	if err := f.blocked(now.Add(refreshBackoffBase + time.Second)); err != nil {
		t.Fatalf("expected no block after first backoff window: %v", err)
	}

	f.record(now, errors.New("dial tcp: network unreachable"))
	if got, want := f.until.Sub(now), 2*refreshBackoffBase; got != want {
		t.Fatalf("second failure backoff = %v, want %v", got, want)
	}

	f.reset()
	f.record(now, errors.New(`token endpoint returned non-2xx status: 400 body={"error":"invalid_grant"}`))
	if got, want := f.until.Sub(now), refreshBackoffMax; got != want {
		t.Fatalf("invalid_grant should use the maximum backoff, got %v want %v", got, want)
	}
	if err := f.blocked(now.Add(refreshBackoffMax - time.Minute)); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("blocked error should wrap the original: %v", err)
	}
}

func TestEnsureValidSkypeTokenSuppressesRepeatedRefreshFailures(t *testing.T) {
	var hits atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS700082: The refresh token has expired"}`))
	}))
	defer tokenServer.Close()

	origFactory := newAuthClient
	newAuthClient = func(store *auth.CookieStore) *auth.Client {
		client := auth.NewClient(store)
		client.TokenEndpoint = tokenServer.URL
		return client
	}
	defer func() { newAuthClient = origFactory }()

	c := &TeamsClient{
		Login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{}},
		Meta: &teamsid.UserLoginMetadata{
			RefreshToken:        "dead-refresh-token",
			SkypeToken:          "expired",
			SkypeTokenExpiresAt: time.Now().Add(-time.Hour).Unix(),
		},
	}
	c.loggedIn.Store(true)

	err := c.ensureValidSkypeToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected invalid_grant error, got %v", err)
	}
	if c.loggedIn.Load() {
		t.Fatalf("a permanent refresh failure should mark the client logged out")
	}

	for i := 0; i < 5; i++ {
		err = c.ensureValidSkypeToken(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refresh suppressed until") {
			t.Fatalf("call %d: expected suppressed error, got %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("token endpoint should be hit once while suppressed, got %d", got)
	}
}

func TestEnsureValidSkypeTokenRefreshUpdatesCachedConsumer(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"mbi-access","refresh_token":"rotated","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	skypeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"skypeToken":{"skypetoken":"fresh-skype","expiresIn":86400,"skypeid":"live:tester"}}`))
	}))
	defer skypeServer.Close()

	origFactory := newAuthClient
	newAuthClient = func(store *auth.CookieStore) *auth.Client {
		client := auth.NewClient(store)
		client.TokenEndpoint = tokenServer.URL
		client.SkypeTokenEndpoint = skypeServer.URL
		return client
	}
	defer func() { newAuthClient = origFactory }()

	c := &TeamsClient{
		Login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{}},
		Meta: &teamsid.UserLoginMetadata{
			RefreshToken:        "old-refresh",
			SkypeToken:          "stale-skype",
			SkypeTokenExpiresAt: time.Now().Add(-time.Hour).Unix(),
		},
	}
	consumer := c.newConsumer()
	c.api = consumer
	if consumer.Token != "stale-skype" {
		t.Fatalf("consumer should start with the stored token, got %q", consumer.Token)
	}

	if err := c.ensureValidSkypeToken(context.Background()); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if consumer.Token != "fresh-skype" {
		t.Fatalf("cached consumer should receive the refreshed token, got %q", consumer.Token)
	}
	if c.Meta.RefreshToken != "rotated" {
		t.Fatalf("rotated refresh token not stored: %q", c.Meta.RefreshToken)
	}
	if !c.IsLoggedIn() {
		t.Fatalf("client should report logged in after a successful refresh")
	}
}
