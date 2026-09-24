package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
)

// latencyWindow limits the latency log to live messages: anything older
// when handled is catch-up (after a restart or an outage), whose delay says
// nothing about the steady state.
const latencyWindow = 10 * time.Minute

// pollTrigger records why and when a thread poll began, and when its fetch
// returned, so each message it brings in can report where its delay went.
type pollTrigger struct {
	noticed time.Time // when a change was noticed, or the poll began if none was
	by      string    // watch, send, longpoll, or schedule (backstop/discovery)
	fetched time.Time // when the message page came back
}

type pollTriggerKey struct{}

func withPollTrigger(ctx context.Context, t *pollTrigger) context.Context {
	return context.WithValue(ctx, pollTriggerKey{}, t)
}

func pollTriggerFrom(ctx context.Context) *pollTrigger {
	t, _ := ctx.Value(pollTriggerKey{}).(*pollTrigger)
	return t
}

// messageLatency splits a message's delay from its Teams send time to the
// moment Matrix had it (handled) into the time until the bridge noticed the
// change, until the page was fetched, and until bridgev2 had sent it.
type messageLatency struct {
	total, toNotice, toFetch, toMatrix time.Duration
}

func splitLatency(sent time.Time, t pollTrigger, handled time.Time) messageLatency {
	clamp := func(d time.Duration) time.Duration { return max(d, 0) }
	return messageLatency{
		total:    clamp(handled.Sub(sent)),
		toNotice: clamp(t.noticed.Sub(sent)),
		toFetch:  clamp(t.fetched.Sub(t.noticed)),
		toMatrix: clamp(handled.Sub(t.fetched)),
	}
}

// latencyLogger returns a PostHandleFunc that logs how long a live message
// sent at sent took to reach Matrix, and where the time went, or nil when
// the poll carries no trigger (the startup catch-up).
func (c *TeamsClient) latencyLogger(ctx context.Context, threadID, messageID string, sent time.Time) func(context.Context, *bridgev2.Portal) {
	trigger := pollTriggerFrom(ctx)
	if trigger == nil || sent.IsZero() {
		return nil
	}
	t := *trigger
	return func(context.Context, *bridgev2.Portal) {
		handled := time.Now()
		if handled.Sub(sent) > latencyWindow || t.fetched.IsZero() {
			return
		}
		l := splitLatency(sent, t, handled)
		log := c.log()
		log.Info().
			Str("thread_id", threadID).
			Str("message_id", messageID).
			Str("noticed_by", t.by).
			Dur("total", l.total).
			Dur("to_notice", l.toNotice).
			Dur("to_fetch", l.toFetch).
			Dur("to_matrix", l.toMatrix).
			Msg("Teams message latency")
	}
}
