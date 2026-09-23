package connector

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"

	"maunium.net/go/mautrix/bridgev2/status"
)

// teamsReachTrip is how many Teams requests in a row must go unanswered
// before the bridge reports TRANSIENT_DISCONNECT. One timeout is noise;
// several back to back mean the route changed, the network went, or Teams
// is down. bridgev2 waits a further three minutes before posting the notice
// the Emacs side turns into a mode-line warning, and CONNECTED follows as
// soon as a request gets through again.
const teamsReachTrip = 3

// teamsReach tracks whether requests to Teams are getting through. Answers
// of any kind count as reachable; an HTTP error still came from Teams.
type teamsReach struct {
	mu       sync.Mutex
	failures int
	down     bool
}

// note records the outcome of one Teams request and returns the bridge
// state to report because of it, or nil when nothing changed.
func (r *teamsReach) note(err error) *status.BridgeState {
	if errors.Is(err, context.Canceled) {
		return nil // the bridge is shutting down, not the network
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if isTeamsNetworkError(err) {
		r.failures++
		if r.failures < teamsReachTrip || r.down {
			return nil
		}
		r.down = true
		return &status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      "teams-unreachable",
			Message:    "Teams is not answering: " + shortNetworkError(err),
		}
	}
	r.failures = 0
	if !r.down {
		return nil
	}
	r.down = false
	return &status.BridgeState{StateEvent: status.StateConnected}
}

// noteTeamsResult feeds a request's outcome to the reachability tracker and
// reports any change of state to bridgev2.
func (c *TeamsClient) noteTeamsResult(err error) {
	if c == nil {
		return
	}
	if state := c.reach.note(err); state != nil && c.Login != nil {
		c.Login.BridgeState.Send(*state)
	}
}

// isTeamsNetworkError reports whether err means the request never got an
// answer: a dial or TLS failure, a timeout, a lost connection.
func isTeamsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// shortNetworkError strips the request URL that net/http wraps around
// transport errors, which is long and names the thread being polled.
func shortNetworkError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return err.Error()
}
