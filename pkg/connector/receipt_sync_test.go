package connector

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func TestRemoteHorizonsIncludesEveryOtherParticipant(t *testing.T) {
	threadID := "19:group@thread.v2"
	resp := &model.ConsumptionHorizonsResponse{
		Horizons: []model.ConsumptionHorizon{
			{ID: "8:orgid:self", ConsumptionHorizon: "1;1;1"},
			{ID: "8:orgid:alice", ConsumptionHorizon: "2;2;2"},
			{ID: threadID, ConsumptionHorizon: "3;3;3"},
			{ID: "8:orgid:bob", ConsumptionHorizon: "4;4;4"},
			{ID: "", ConsumptionHorizon: "5;5;5"},
		},
	}
	got := remoteHorizons(resp, "8:orgid:self", threadID)
	if len(got) != 2 {
		t.Fatalf("expected alice and bob, got %d entries: %+v", len(got), got)
	}
	if got[0].id != "8:orgid:alice" || got[1].id != "8:orgid:bob" {
		t.Fatalf("unexpected participants: %s, %s", got[0].id, got[1].id)
	}
	if got[1].horizon == nil || got[1].horizon.ConsumptionHorizon != "4;4;4" {
		t.Fatalf("horizon pointer should reference the response entry")
	}
	if remoteHorizons(nil, "8:orgid:self", threadID) != nil {
		t.Fatalf("nil response should yield nil")
	}
}

func TestOwnHorizonFindsSelf(t *testing.T) {
	resp := &model.ConsumptionHorizonsResponse{
		Horizons: []model.ConsumptionHorizon{
			{ID: "8:orgid:alice", ConsumptionHorizon: "2;2;2"},
			{ID: "8:orgid:self", ConsumptionHorizon: "1;1;1"},
		},
	}
	if got := ownHorizon(resp, "8:orgid:self"); got == nil || got.ConsumptionHorizon != "1;1;1" {
		t.Fatalf("got %+v", got)
	}
	if ownHorizon(resp, "8:orgid:bob") != nil || ownHorizon(nil, "8:orgid:self") != nil {
		t.Fatal("expected nil")
	}
}

func TestOwnHorizonNeedsDoublePuppet(t *testing.T) {
	sink := &capturingEventSink{}
	c, _ := newBridgeTestClient(t, &mockTeamsAPI{}, sink)
	ctx := context.Background()
	h := &model.ConsumptionHorizon{ID: testSelfUserID, ConsumptionHorizon: "5;5000;0"}
	if err := c.syncOwnHorizon(ctx, zerolog.Nop(), testThreadID, testSelfUserID, h); err != nil {
		t.Fatal(err)
	}
	if len(sink.getEvents()) != 0 {
		t.Fatalf("receipt sent with no double puppet: %v", sink.getEvents())
	}
	// Not recorded either, so it goes out once double puppeting is set up.
	if state, err := c.Main.DB.ConsumptionHorizon.Get(ctx, c.Login.ID, testThreadID, testSelfUserID); err != nil || state != nil {
		t.Fatalf("state %+v, err %v", state, err)
	}
}

func TestMatrixReceiptCoveredByOwnHorizonNotSent(t *testing.T) {
	api := &mockTeamsAPI{}
	c, _ := newBridgeTestClient(t, api, &capturingEventSink{})
	ctx := context.Background()
	read := time.UnixMilli(1_700_000_000_000)
	if err := c.Main.DB.ConsumptionHorizon.UpsertLastRead(ctx, c.Login.ID, outDMThread, testSelfUserID, read.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c.markUnread(outDMThread)
	portal := newTestPortal(networkid.PortalID(outDMThread), "")

	// The echo of a Teams-side read: already read there.
	if err := c.HandleMatrixReadReceipt(ctx, &bridgev2.MatrixReadReceipt{Portal: portal, ReadUpTo: read}); err != nil || len(api.setHorizons) != 0 {
		t.Fatalf("err %v, horizons %v", err, api.setHorizons)
	}
	// Read further on Matrix: sent, and recorded so the poll does not echo it.
	before := time.Now().UnixMilli()
	if err := c.HandleMatrixReadReceipt(ctx, &bridgev2.MatrixReadReceipt{Portal: portal, ReadUpTo: read.Add(time.Second)}); err != nil || len(api.setHorizons) != 1 {
		t.Fatalf("err %v, horizons %v", err, api.setHorizons)
	}
	state, err := c.Main.DB.ConsumptionHorizon.Get(ctx, c.Login.ID, outDMThread, testSelfUserID)
	if err != nil || state == nil || state.LastReadTS < before {
		t.Fatalf("state %+v, err %v", state, err)
	}
}
