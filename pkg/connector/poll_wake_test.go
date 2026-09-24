package connector

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func TestApplyWakeupMatchesThreadOrConversation(t *testing.T) {
	c := &TeamsClient{}
	later := time.Now().Add(time.Minute)
	states := map[string]*pollState{
		"19:a@thread.v2": {nextPoll: later, backoff: PollBackoff{Delay: 30 * time.Second}},
		"19:b@thread.v2": {nextPoll: later, conversation: "48:conv", backoff: PollBackoff{Delay: 30 * time.Second}},
		"19:c@thread.v2": {nextPoll: later},
	}
	if !c.applyWakeup(pollWakeup{threadID: "19:a@thread.v2"}, states) {
		t.Fatal("thread ID did not match")
	}
	if !c.applyWakeup(pollWakeup{threadID: "48:conv"}, states) {
		t.Fatal("conversation ID did not match")
	}
	if c.applyWakeup(pollWakeup{threadID: "19:unknown"}, states) {
		t.Fatal("unknown thread matched")
	}
	for _, id := range []string{"19:a@thread.v2", "19:b@thread.v2"} {
		if !states[id].nextPoll.IsZero() || states[id].backoff.Delay != pollBaseDelay {
			t.Errorf("%s not made due at the base cadence: %+v", id, states[id])
		}
	}
	if states["19:c@thread.v2"].nextPoll != later {
		t.Error("unrelated thread was woken")
	}
}

func TestRequestPollNeverBlocks(t *testing.T) {
	c := &TeamsClient{}
	for i := 0; i < wakeQueueSize+10; i++ {
		c.requestPoll("19:a@thread.v2", false, "test")
	}
	if got := len(c.wakeChan()); got != wakeQueueSize {
		t.Fatalf("queue holds %d, want %d", got, wakeQueueSize)
	}
}

func TestReceiptIntervalFollowsActivity(t *testing.T) {
	c := &TeamsClient{}
	now := time.Now()
	if !c.shouldPollReceipts("t", now) {
		t.Fatal("first check refused")
	}
	if c.shouldPollReceipts("t", now.Add(time.Minute)) {
		t.Fatal("idle thread checked again within the idle interval")
	}
	c.noteThreadActive("t", now.Add(time.Minute))
	if !c.shouldPollReceipts("t", now.Add(time.Minute+time.Second)) ||
		!c.shouldPollReceipts("t", now.Add(time.Minute+2*time.Second)) {
		t.Fatal("active thread not checked on every poll")
	}
	if !c.shouldPollReceipts("t", now.Add(threadActiveWindow+2*time.Minute)) {
		t.Fatal("expired activity: due check after the idle interval refused")
	}
}

func TestIdleCapFor(t *testing.T) {
	c := &TeamsClient{}
	now := time.Now()
	if got := c.idleCapFor("t", now); got != pollIdleCap {
		t.Errorf("unwatched: %v", got)
	}
	c.activityUp.Store(true)
	if got := c.idleCapFor("t", now); got != pollBackstopIdleCap {
		t.Errorf("watched: %v", got)
	}
	c.noteThreadActive("t", now)
	if got := c.idleCapFor("t", now.Add(time.Minute)); got != activeThreadIdleCap {
		t.Errorf("active: %v", got)
	}
}

func TestCheckActivityWakesChangedThreads(t *testing.T) {
	api := &recentConvsAPI{}
	c := &TeamsClient{}
	c.api = api
	later := time.Now().Add(time.Hour)
	states := map[string]*pollState{
		"19:a": {nextPoll: later}, "19:b": {nextPoll: later}, "19:c": {nextPoll: later},
	}
	lastSeen := map[string]string{}
	api.set("19:a", "1", "19:b", "5")
	c.checkActivity(context.Background(), states, lastSeen)
	for id, ps := range states {
		if ps.nextPoll != later {
			t.Fatalf("first check woke %s", id)
		}
	}
	if !c.activityUp.Load() {
		t.Fatal("watch not marked up")
	}
	// a changes, b does not, c appears for the first time.
	api.set("19:a", "2", "19:b", "5", "19:c", "9")
	c.checkActivity(context.Background(), states, lastSeen)
	if !states["19:a"].nextPoll.IsZero() || !states["19:c"].nextPoll.IsZero() {
		t.Error("changed or newly listed thread not woken")
	}
	if states["19:b"].nextPoll != later {
		t.Error("unchanged thread woken")
	}
}

func TestIdleCapRisesWhileLongPollIsUp(t *testing.T) {
	b := PollBackoff{Delay: pollIdleCap, IdleCap: pollBackstopIdleCap}
	for i := 0; i < int(pollBackstopIdleCap/pollBaseDelay)+1; i++ {
		b.OnIdle()
	}
	if b.Delay != pollBackstopIdleCap {
		t.Fatalf("delay %v, want %v", b.Delay, pollBackstopIdleCap)
	}
}

// recentConvsAPI answers ListRecentConversations with a fixed list.
type recentConvsAPI struct {
	mockTeamsAPI
	convs []model.RemoteConversation
}

// set replaces the list with (conversation ID, newest message ID) pairs.
func (a *recentConvsAPI) set(pairs ...string) {
	a.convs = nil
	for i := 0; i+1 < len(pairs); i += 2 {
		conv := model.RemoteConversation{ID: pairs[i]}
		conv.LastMessage.ID = pairs[i+1]
		a.convs = append(a.convs, conv)
	}
}

func (a *recentConvsAPI) ListRecentConversations(context.Context, string, int) ([]model.RemoteConversation, error) {
	return a.convs, nil
}
