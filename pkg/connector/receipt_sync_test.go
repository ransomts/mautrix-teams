package connector

import (
	"testing"

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
