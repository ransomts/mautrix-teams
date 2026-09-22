package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

func TestSupersededFollowsTheLoginsClient(t *testing.T) {
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{}}
	first := &TeamsClient{Login: login}
	login.Client = first
	if first.superseded() {
		t.Fatalf("the login's own client is not superseded")
	}
	second := &TeamsClient{Login: login}
	login.Client = second
	if !first.superseded() {
		t.Fatalf("a client replaced on its login should be superseded")
	}
	if second.superseded() {
		t.Fatalf("the replacement is the login's client")
	}
	var none *TeamsClient
	if none.superseded() || (&TeamsClient{}).superseded() {
		t.Fatalf("no login, nothing to be superseded by")
	}
}

func TestSupersededClientDoesNotRefreshTokens(t *testing.T) {
	var hits atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"mbi-access","refresh_token":"rotated","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	origFactory := newAuthClient
	newAuthClient = func(store *auth.CookieStore) *auth.Client {
		client := auth.NewClient(store)
		client.TokenEndpoint = tokenServer.URL
		return client
	}
	defer func() { newAuthClient = origFactory }()

	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{}}
	old := &TeamsClient{
		Login: login,
		Meta: &teamsid.UserLoginMetadata{
			RefreshToken:        "old-refresh-token",
			SkypeToken:          "expired",
			SkypeTokenExpiresAt: time.Now().Add(-time.Hour).Unix(),
		},
	}
	login.Client = &TeamsClient{Login: login}

	if err := old.ensureValidSkypeToken(context.Background()); !errors.Is(err, errClientSuperseded) {
		t.Fatalf("expected errClientSuperseded, got %v", err)
	}
	if err := old.ensureValidGraphToken(context.Background()); !errors.Is(err, errClientSuperseded) {
		t.Fatalf("expected errClientSuperseded from the graph refresh, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("a superseded client must not touch the token endpoint, got %d hits", got)
	}
	if !loopStopped(errClientSuperseded) || !loopStopped(context.Canceled) || loopStopped(errors.New("boom")) {
		t.Fatalf("loopStopped should recognise only the deliberate stops")
	}
}

func TestBadCredentialsReportedOncePerFailure(t *testing.T) {
	var b badCredentials
	blocked := errors.New("token endpoint returned non-2xx status: 400 (refresh suppressed until 2026-09-21T23:30:03Z)")
	if msg, changed := b.note(blocked); !changed || msg != "token endpoint returned non-2xx status: 400" {
		t.Fatalf("first failure should be sent without its backoff deadline, got %q changed=%v", msg, changed)
	}
	later := errors.New("token endpoint returned non-2xx status: 400 (refresh suppressed until 2026-09-21T23:45:03Z)")
	if _, changed := b.note(later); changed {
		t.Fatalf("the backoff deadline moving is not a new failure")
	}
	other := errors.New("dial tcp: lookup login.microsoftonline.com: server misbehaving")
	if _, changed := b.note(other); !changed {
		t.Fatalf("a different failure should be sent")
	}
	if !b.clear() {
		t.Fatalf("clearing an outstanding failure reports it")
	}
	if b.clear() {
		t.Fatalf("nothing outstanding after a clear")
	}
	if _, changed := b.note(other); !changed {
		t.Fatalf("the same failure after a clear is outstanding again")
	}
}
