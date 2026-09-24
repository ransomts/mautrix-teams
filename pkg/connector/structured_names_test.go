package connector

import (
	"context"
	"testing"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

func TestChannelRoomName(t *testing.T) {
	const tid = "19:chan@thread.tacv2"
	cases := []struct {
		name     string
		info     *graph.ChannelInfo
		baseName string
		want     string
	}{
		{"team and channel", &graph.ChannelInfo{TeamName: "CS", ChannelName: "General"}, "old", "CS / General"},
		{"channel falls back to base", &graph.ChannelInfo{TeamName: "CS"}, "Base", "CS / Base"},
		{"channel only", &graph.ChannelInfo{ChannelName: "General"}, "", "General"},
		{"team only", &graph.ChannelInfo{TeamName: "CS"}, "", "CS"},
		{"empty info uses base", &graph.ChannelInfo{}, "Base", "Base"},
		{"empty info no base", &graph.ChannelInfo{}, "", tid},
		{"not in map uses base", nil, "Base", "Base"},
		{"not in map no base", nil, "", tid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelMap := map[string]graph.ChannelInfo{}
			if tc.info != nil {
				channelMap[tid] = *tc.info
			}
			if got := channelRoomName(channelMap, tid, tc.baseName); got != tc.want {
				t.Fatalf("channelRoomName = %q, want %q", got, tc.want)
			}
		})
	}
}

// The non-DM cases of structuredRoomName need no lookups.
func TestStructuredRoomNameNonDM(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	channelMap := map[string]graph.ChannelInfo{"19:chan@thread.tacv2": {TeamName: "CS", ChannelName: "General"}}
	cases := []struct {
		threadID, name, want string
	}{
		{"19:meeting_abc@thread.v2", "Standup", "Meeting: Standup"},
		{"19:meeting_abc@thread.v2", "", "Meeting: Meeting"},
		{"19:meeting_abc@thread.v2", "Chat", "Meeting: Meeting"},
		{"19:chan@thread.tacv2", "General", "CS / General"},
		{"19:group@thread.v2", "Friends", "Group: Friends"},
		{"19:group@thread.v2", "", ""},
		{"19:group@thread.v2", "Chat", ""},
	}
	for _, tc := range cases {
		th := &teamsdb.ThreadState{ThreadID: tc.threadID, Name: tc.name}
		if got := c.structuredRoomName(context.Background(), th, tc.threadID, channelMap); got != tc.want {
			t.Errorf("structuredRoomName(%q, %q) = %q, want %q", tc.threadID, tc.name, got, tc.want)
		}
	}
}
