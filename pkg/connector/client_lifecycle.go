package connector

// A client's place on its login: whether a re-login has replaced it, and
// the credential failure it has reported.

import (
	"errors"
	"strings"
	"sync"

	"maunium.net/go/mautrix/bridgev2/status"
)

// errClientSuperseded is returned by the loops and token refreshes of a
// client that a re-login has replaced; see TeamsClient.superseded.
var errClientSuperseded = errors.New("client superseded by a re-login")

// superseded reports whether a newer client has replaced this one on its
// login.  A re-login for the same account (the keep-alive timer does one
// every eight hours) reuses the UserLogin and gives it a new client without
// disconnecting the old one.  Left alone, the old client polls on with the
// old tokens, reports their failures as the login's state and, when its own
// refresh happens to succeed, saves the old tokens over the new ones.
func (c *TeamsClient) superseded() bool {
	if c == nil || c.Login == nil {
		return false
	}
	current, ok := c.Login.Client.(*TeamsClient)
	return ok && current != nil && current != c
}

// badCredentials remembers the BAD_CREDENTIALS state a client has reported,
// so that one failed token refresh is announced once rather than once per
// thread poll: with a few hundred threads and a cached failure answering
// every poll at once, per-thread sends overflowed bridgev2's state queue
// and rolled the log over every few minutes.
type badCredentials struct {
	mu      sync.Mutex
	message string // the outstanding failure, "" when none
}

// key is err's message without the backoff deadline a cached refresh
// failure carries, so the deadline moving does not count as a new failure.
func badCredentialsKey(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, " (refresh suppressed until "); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// note records err and reports whether it differs from the outstanding
// failure, in which case it is worth sending.
func (b *badCredentials) note(err error) (string, bool) {
	msg := badCredentialsKey(err)
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := msg != b.message
	b.message = msg
	return msg, changed
}

// clear forgets the outstanding failure and reports whether there was one.
func (b *badCredentials) clear() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	had := b.message != ""
	b.message = ""
	return had
}

// reportBadCredentials sends a BAD_CREDENTIALS state for err unless the same
// failure is already outstanding.  A superseded client says nothing: the
// state belongs to the client that replaced it.
func (c *TeamsClient) reportBadCredentials(err error) {
	if c == nil || c.Login == nil || err == nil || c.superseded() {
		return
	}
	if msg, changed := c.badCreds.note(err); changed {
		c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateBadCredentials, Message: msg, UserAction: status.UserActionRelogin})
	}
}

// clearBadCredentials reports CONNECTED when a failure was outstanding and
// the tokens are valid again.
func (c *TeamsClient) clearBadCredentials() {
	if c == nil || c.Login == nil {
		return
	}
	if c.badCreds.clear() && !c.superseded() {
		c.Login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	}
}
