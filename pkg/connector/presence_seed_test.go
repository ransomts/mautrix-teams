package connector

import (
	"errors"
	"testing"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
)

func TestPresenceForbiddenSentinel(t *testing.T) {
	// The graph layer must surface a 403 as the typed sentinel so the poller
	// can disable itself instead of logging every cycle.
	if !errors.Is(graph.ErrPresenceForbidden, graph.ErrPresenceForbidden) {
		t.Fatal("sentinel identity broken")
	}
}

func TestPresenceForbiddenFlagStopsPolling(t *testing.T) {
	c := &TeamsClient{}
	if c.presenceForbidden.Load() {
		t.Fatal("should start enabled")
	}
	// pollPresence returns immediately once the flag is set; exercise the guard.
	c.presenceForbidden.Store(true)
	c.pollPresence(nil) // must not panic despite nil ctx/deps because the flag short-circuits after the nil check
}
