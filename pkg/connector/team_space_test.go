package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestTeamSpaceChatInfoFallsBackToPortalName(t *testing.T) {
	c := &TeamsClient{}
	portal := &bridgev2.Portal{Portal: &database.Portal{Name: "SoC Faculty Advising"}}
	info := c.teamSpaceChatInfo(context.Background(), "abc", portal)
	if info == nil || info.Name == nil || *info.Name != "SoC Faculty Advising" {
		t.Fatalf("expected the portal's existing name, got %+v", info)
	}
	if info.Type == nil || *info.Type != database.RoomTypeSpace {
		t.Fatalf("team portals must be spaces")
	}
	empty := c.teamSpaceChatInfo(context.Background(), "abc", &bridgev2.Portal{Portal: &database.Portal{}})
	if *empty.Name != "Team" {
		t.Fatalf("expected generic fallback, got %q", *empty.Name)
	}
}
