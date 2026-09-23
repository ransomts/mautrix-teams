package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/rs/zerolog"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

func TestPollFailureLevel(t *testing.T) {
	live := context.Background()
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	boom := consumerclient.MessagesError{Status: http.StatusInternalServerError, BodySnippet: "boom"}
	// What the HTTP client returns when a re-login cancels the old client mid-request.
	cut := &url.Error{Op: "Get", URL: "https://teams.invalid/messages", Err: context.Canceled}

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		down bool
		want zerolog.Level
	}{
		{"server error", live, boom, false, zerolog.WarnLevel},
		{"request cancelled", live, cut, false, zerolog.DebugLevel},
		{"client stopping", stopped, boom, false, zerolog.DebugLevel},
		{"superseded", live, fmt.Errorf("poll: %w", errClientSuperseded), false, zerolog.DebugLevel},
		{"thread gone", live, consumerclient.MessagesError{Status: http.StatusNotFound, BodySnippet: `{"errorCode":"ThreadNotFound"}`}, false, zerolog.DebugLevel},
		{"teams unreachable", live, boom, true, zerolog.DebugLevel},
	}
	for _, tc := range cases {
		c := &TeamsClient{}
		if tc.down {
			c.reach.markDown(errors.New("offline"))
		}
		if got := c.pollFailureLevel(tc.ctx, tc.err); got != tc.want {
			t.Errorf("%s: level %v, want %v", tc.name, got, tc.want)
		}
	}
}
