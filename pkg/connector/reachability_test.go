package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"maunium.net/go/mautrix/bridgev2/status"
)

type reachTimeout struct{}

func (reachTimeout) Error() string   { return "i/o timeout" }
func (reachTimeout) Timeout() bool   { return true }
func (reachTimeout) Temporary() bool { return true }

func TestTeamsReach_TripsAfterConsecutiveNetworkErrors(t *testing.T) {
	var r teamsReach
	netErr := &url.Error{Op: "Get", URL: "https://example.invalid/x", Err: reachTimeout{}}
	for i := 1; i < teamsReachTrip; i++ {
		if state := r.note(netErr); state != nil {
			t.Fatalf("failure %d reported %v before the trip point", i, state.StateEvent)
		}
	}
	state := r.note(netErr)
	if state == nil || state.StateEvent != status.StateTransientDisconnect {
		t.Fatalf("expected TRANSIENT_DISCONNECT at failure %d, got %v", teamsReachTrip, state)
	}
	if state.Message != "Teams is not answering: i/o timeout" {
		t.Errorf("unexpected message %q", state.Message)
	}
	if again := r.note(netErr); again != nil {
		t.Errorf("a further failure while down must not report again, got %v", again.StateEvent)
	}
	if state := r.note(nil); state == nil || state.StateEvent != status.StateConnected {
		t.Fatalf("expected CONNECTED once a request succeeds, got %v", state)
	}
	if state := r.note(nil); state != nil {
		t.Errorf("success while up must not report, got %v", state.StateEvent)
	}
}

func TestTeamsReach_AnswersResetTheCount(t *testing.T) {
	var r teamsReach
	netErr := &url.Error{Op: "Get", URL: "https://example.invalid/x", Err: reachTimeout{}}
	httpErr := errors.New("teams returned 400")
	for i := 0; i < 10; i++ {
		if state := r.note(netErr); state != nil {
			t.Fatal("tripped despite answers in between")
		}
		if state := r.note(httpErr); state != nil {
			t.Fatal("an HTTP error is an answer and must not change state")
		}
	}
}

func TestTeamsReach_IgnoresCancellation(t *testing.T) {
	var r teamsReach
	for i := 0; i < teamsReachTrip+1; i++ {
		if state := r.note(context.Canceled); state != nil {
			t.Fatal("context cancellation is not a network failure")
		}
	}
	if state := r.note(context.DeadlineExceeded); state != nil {
		t.Fatal("one deadline is below the trip point")
	}
}

func TestTeamsReach_MarkDownReportsAtOnce(t *testing.T) {
	var r teamsReach
	netErr := &url.Error{Op: "Post", URL: "https://login.example.invalid/token", Err: reachTimeout{}}
	state := r.markDown(netErr)
	if state == nil || state.StateEvent != status.StateTransientDisconnect {
		t.Fatalf("expected TRANSIENT_DISCONNECT on the first failure, got %v", state)
	}
	if again := r.markDown(netErr); again != nil {
		t.Errorf("already down, must not report again, got %v", again.StateEvent)
	}
	if again := r.note(netErr); again != nil {
		t.Errorf("a further failure while down must not report, got %v", again.StateEvent)
	}
	if state := r.note(nil); state == nil || state.StateEvent != status.StateConnected {
		t.Fatalf("expected CONNECTED once a request gets through, got %v", state)
	}
}

func TestReportTokenErrorSeparatesNetworkFromLogin(t *testing.T) {
	c := &TeamsClient{}
	netErr := &url.Error{Op: "Post", URL: "https://login.example.invalid/token", Err: reachTimeout{}}
	c.reportTokenError(fmt.Errorf("%w (refresh suppressed until 2026-09-23T20:00:00Z)", netErr))
	if c.reach.failures != 1 {
		t.Fatalf("a refresh that got no answer should count against reachability, failures=%d", c.reach.failures)
	}
	c.reportTokenError(errors.New(`token endpoint returned non-2xx status: 400 body={"error":"invalid_grant"}`))
	if c.reach.failures != 0 {
		t.Fatalf("an answer from the identity provider means it is reachable, failures=%d", c.reach.failures)
	}
}
