package connector

// Limits on how hard the poll loop leans on Teams: pausing everything when
// Teams rate-limits, parking threads Teams says no longer exist, spacing
// failed change checks, and how often the thread list is re-read.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

const (
	// throttleDefault is the pause after a 429 that names no Retry-After.
	throttleDefault = 30 * time.Second
	// throttleMax caps a server-requested pause.
	throttleMax = 10 * time.Minute
	// goneThreadRecheck is how often a thread Teams reports as deleted is
	// polled again, in case it comes back. A change seen by the recent
	// conversations watch polls it at once anyway.
	goneThreadRecheck = 6 * time.Hour
	// activityFailureMax caps the spacing of failing change checks.
	activityFailureMax = time.Minute
	// discoveryIntervalWatched is how often the full conversation list is
	// re-read while the recent-conversations watch works: new chats show up
	// in the watch first and trigger discovery themselves.
	discoveryIntervalWatched = 5 * time.Minute
	// discoveryMinGap spaces discoveries triggered by unknown conversations,
	// so one that discovery never adds cannot make it run every check.
	discoveryMinGap = 30 * time.Second
)

// teamsThrottle reports whether err is Teams rate-limiting (HTTP 429) and,
// if so, how long to pause all polling. Teams limits per user, so one
// throttled request means every request should wait.
func teamsThrottle(err error) (time.Duration, bool) {
	var retryable consumerclient.RetryableError
	if !errors.As(err, &retryable) || retryable.Status != http.StatusTooManyRequests {
		return 0, false
	}
	if retryable.RetryAfter <= 0 {
		return throttleDefault, true
	}
	return min(retryable.RetryAfter, throttleMax), true
}

// isThreadGone reports whether a poll failed because Teams no longer has
// the thread: deleted, or soft-deleted (left or hidden). Only these
// explicit answers count, so a 404 from a wrong endpoint does not park
// every thread.
func isThreadGone(err error) bool {
	var msgErr consumerclient.MessagesError
	if !errors.As(err, &msgErr) || msgErr.Status != http.StatusNotFound {
		return false
	}
	return strings.Contains(msgErr.BodySnippet, "ThreadNotFound") ||
		strings.Contains(msgErr.BodySnippet, "SoftDeleted")
}

// activityRetryDelay spaces change checks after failures in a row.
func activityRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return activityCheckInterval
	}
	return min(activityCheckInterval<<min(failures, 6), activityFailureMax)
}

// discoveryInterval is how long until the next full conversation-list read.
func (c *TeamsClient) discoveryInterval() time.Duration {
	if c.activityUp.Load() || c.longPollUp.Load() {
		return discoveryIntervalWatched
	}
	return threadDiscoveryInterval
}
