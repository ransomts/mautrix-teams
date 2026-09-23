package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

func TestTeamsThrottle(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want time.Duration
		ok   bool
	}{
		{"retry-after honoured", consumerclient.RetryableError{Status: 429, RetryAfter: 45 * time.Second}, 45 * time.Second, true},
		{"no retry-after", consumerclient.RetryableError{Status: 429}, throttleDefault, true},
		{"retry-after capped", consumerclient.RetryableError{Status: 429, RetryAfter: time.Hour}, throttleMax, true},
		{"wrapped", fmt.Errorf("poll: %w", consumerclient.RetryableError{Status: 429}), throttleDefault, true},
		{"server error is not a throttle", consumerclient.RetryableError{Status: 503}, 0, false},
		{"other error", errors.New("boom"), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := teamsThrottle(tc.err)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsThreadGone(t *testing.T) {
	gone := []error{
		consumerclient.MessagesError{Status: http.StatusNotFound, BodySnippet: `{"errorCode":404,"message":"{\"subCode\":\"ThreadNotFound\"}"}`},
		consumerclient.MessagesError{Status: http.StatusNotFound, BodySnippet: `{"errorCode":404,"message":"{\"subCode\":\"SoftDeleted\"}"}`},
	}
	for _, err := range gone {
		if !isThreadGone(err) {
			t.Errorf("expected gone: %v", err)
		}
	}
	notGone := []error{
		// A bare 404 might be a wrong endpoint, which must not park threads.
		consumerclient.MessagesError{Status: http.StatusNotFound, BodySnippet: `{"errorCode":404}`},
		consumerclient.MessagesError{Status: http.StatusBadRequest, BodySnippet: "ThreadNotFound"},
		consumerclient.RetryableError{Status: 503},
		nil,
	}
	for _, err := range notGone {
		if isThreadGone(err) {
			t.Errorf("expected not gone: %v", err)
		}
	}
}

func TestActivityRetryDelay(t *testing.T) {
	if got := activityRetryDelay(0); got != activityCheckInterval {
		t.Errorf("no failures: %v", got)
	}
	if got := activityRetryDelay(1); got != 2*activityCheckInterval {
		t.Errorf("one failure: %v", got)
	}
	for _, n := range []int{5, 6, 20, 1000} {
		if got := activityRetryDelay(n); got > activityFailureMax {
			t.Errorf("%d failures: %v exceeds the cap", n, got)
		}
	}
	if got := activityRetryDelay(1000); got != activityFailureMax {
		t.Errorf("many failures should sit at the cap, got %v", got)
	}
}

func TestDiscoveryIntervalFollowsWatch(t *testing.T) {
	c := &TeamsClient{}
	if got := c.discoveryInterval(); got != threadDiscoveryInterval {
		t.Errorf("unwatched: %v", got)
	}
	c.activityUp.Store(true)
	if got := c.discoveryInterval(); got != discoveryIntervalWatched {
		t.Errorf("watched: %v", got)
	}
}

func TestCheckActivityReportsUnknownConversations(t *testing.T) {
	api := &recentConvsAPI{}
	c := &TeamsClient{}
	c.api = api
	states := map[string]*pollState{"19:a": {nextPoll: time.Now().Add(time.Hour)}}
	lastSeen := map[string]string{}
	api.set("19:a", "1")
	if unknown, err := c.checkActivity(context.Background(), states, lastSeen); err != nil || unknown {
		t.Fatalf("priming check: unknown=%v err=%v", unknown, err)
	}
	// A system stream changing is not a new chat.
	api.set("19:a", "1", "19:x_teamsstream_notifications@thread.v2", "7")
	if unknown, _ := c.checkActivity(context.Background(), states, lastSeen); unknown {
		t.Fatal("a system stream was reported as an unknown chat")
	}
	api.set("19:a", "1", "19:x_teamsstream_notifications@thread.v2", "7", "19:new", "3")
	if unknown, _ := c.checkActivity(context.Background(), states, lastSeen); !unknown {
		t.Fatal("a new conversation was not reported")
	}
	api.set("19:a", "2", "19:x_teamsstream_notifications@thread.v2", "7", "19:new", "3")
	if unknown, _ := c.checkActivity(context.Background(), states, lastSeen); unknown {
		t.Fatal("a known thread's change was reported as unknown")
	}
}
