package connector

import (
	"context"
	"errors"
	"testing"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

type savedFakeAPI struct {
	*mockTeamsAPI
	saved map[string][]consumerclient.SavedMessage
	err   error
	reads []string
}

func (a *savedFakeAPI) GetSavedMessages(_ context.Context, conversationID string) ([]consumerclient.SavedMessage, error) {
	a.reads = append(a.reads, conversationID)
	return a.saved[conversationID], a.err
}

func (a *savedFakeAPI) SetMessageSaved(context.Context, string, string, string, bool) error {
	return nil
}

func TestSavedMessagesReadOnlyWhenVersionChanges(t *testing.T) {
	api := &savedFakeAPI{mockTeamsAPI: &mockTeamsAPI{}, saved: map[string][]consumerclient.SavedMessage{
		"48:notes": {{MessageID: "5", RootID: "5", SavedAt: 10}},
	}}
	c, _ := newBridgeTestClient(t, api, &capturingEventSink{})
	ctx := context.Background()

	c.saved.noteVersion("48:notes", "48:notes", "100")
	c.saved.noteVersion("19:a@thread.v2", "19:a@thread.v2", "")
	c.syncSavedMessages(ctx)
	if len(api.reads) != 1 || len(c.saved.saved["48:notes"]) != 1 {
		t.Fatalf("reads %v, saved %v", api.reads, c.saved.saved)
	}
	// Same version: not read again.
	c.saved.noteVersion("48:notes", "48:notes", "100")
	c.syncSavedMessages(ctx)
	if len(api.reads) != 1 {
		t.Fatalf("read again: %v", api.reads)
	}
	// A failed read is retried when discovery sees the version again.
	api.err = errors.New("boom")
	c.saved.noteVersion("48:notes", "48:notes", "101")
	c.syncSavedMessages(ctx)
	api.err = nil
	api.saved["48:notes"] = nil
	c.saved.noteVersion("48:notes", "48:notes", "101")
	c.syncSavedMessages(ctx)
	if len(api.reads) != 3 || len(c.saved.saved) != 0 {
		t.Fatalf("reads %v, saved %v", api.reads, c.saved.saved)
	}
}
