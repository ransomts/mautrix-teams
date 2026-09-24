package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

func TestSplitLatency(t *testing.T) {
	sent := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	l := splitLatency(sent, pollTrigger{
		noticed: sent.Add(1500 * time.Millisecond),
		fetched: sent.Add(1800 * time.Millisecond),
	}, sent.Add(2100*time.Millisecond))
	if l.total != 2100*time.Millisecond || l.toNotice != 1500*time.Millisecond ||
		l.toFetch != 300*time.Millisecond || l.toMatrix != 300*time.Millisecond {
		t.Fatalf("%+v", l)
	}
	// A poll that began before the message was sent (it arrived during the
	// fetch) has no waiting-to-notice part.
	if l := splitLatency(sent, pollTrigger{noticed: sent.Add(-time.Second), fetched: sent.Add(time.Second)}, sent.Add(2*time.Second)); l.toNotice != 0 {
		t.Fatalf("negative notice time not clamped: %+v", l)
	}
}

func TestWakeupKeepsEarliestNotice(t *testing.T) {
	c := &TeamsClient{}
	states := map[string]*pollState{"19:a": {}}
	first := time.Now()
	c.applyWakeup(pollWakeup{threadID: "19:a", at: first, source: "watch"}, states)
	c.applyWakeup(pollWakeup{threadID: "19:a", at: first.Add(time.Second), source: "send"}, states)
	if ps := states["19:a"]; !ps.noticedAt.Equal(first) || ps.noticedBy != "watch" {
		t.Fatalf("notice %v by %q", ps.noticedAt, ps.noticedBy)
	}
}

func TestLatencyLoggedWhenMatrixHasTheMessage(t *testing.T) {
	var buf bytes.Buffer
	sent := time.Now().Add(-3 * time.Second)
	old := userMessage("102")
	old.Timestamp = time.Now().Add(-time.Hour) // catch-up: not logged
	live := userMessage("101")
	live.Timestamp = sent
	api := &schedAPI{pages: map[string][]model.RemoteMessage{"conv-a": {live, old}}}
	sink := &capturingEventSink{}
	c := newSchedTestClient(t, api, sink, nil)
	c.Main.Log = zerolog.New(&buf)
	addTestThread(t, c, &teamsdb.ThreadState{ThreadID: "19:a", Conversation: "conv-a", LastSequenceID: "100"})

	ctx := withPollTrigger(context.Background(), &pollTrigger{noticed: sent.Add(time.Second), by: "watch"})
	th, err := c.Main.DB.ThreadState.Get(ctx, c.Login.ID, "19:a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.pollThread(ctx, th, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, evt := range sink.getEvents() {
		if evt.GetType() == bridgev2.RemoteEventMessage {
			runPostHandle(t, evt)
		}
	}

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["message"] == "Teams message latency" {
			lines = append(lines, entry)
		}
	}
	if len(lines) != 1 || lines[0]["noticed_by"] != "watch" || lines[0]["message_id"] != "msg-101" {
		t.Fatalf("latency lines %v", lines)
	}
	// The cursor's own hook still ran: the page is saved.
	if got := savedCursor(t, c, "19:a"); got != "102" {
		t.Fatalf("cursor %s, want 102", got)
	}
}
