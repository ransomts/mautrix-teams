package connector

// A channel's Files tab -> Matrix room account data.  The files command
// lists the channel's SharePoint folder through Graph and puts the list in
// the user's account data for that room (fi.mau.teams.channel_files), where
// a client reads it: the files there were uploaded straight to the folder
// as often as posted in the channel, so the bridged attachments are only
// part of them.

import (
	"context"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
)

const channelFilesAccountDataType = "fi.mau.teams.channel_files"

// threadPropertiesAPI is the part of the Teams client that reads a
// thread's properties.
type threadPropertiesAPI interface {
	GetThreadProperties(ctx context.Context, threadID string) (map[string]string, error)
}

var cmdFiles = &commands.FullHandler{
	Func: fnFiles,
	Name: "files",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Fetch a channel's Files tab (its SharePoint folder) into the room's account data.",
		Args:        "[<room ID>]",
	},
	RequiresLogin: true,
}

func fnFiles(ce *commands.Event) {
	portal := ce.Portal
	if len(ce.Args) == 1 {
		var err error
		if portal, err = ce.Bridge.GetPortalByMXID(ce.Ctx, id.RoomID(ce.Args[0])); err != nil {
			portal = nil
		}
	}
	if portal == nil || portal.MXID == "" {
		ce.Reply("Run this in a channel's room, or name it: `$cmdprefix files <room ID>`")
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	channelID := string(portal.ID)
	if !isChannelThread(channelID) {
		ce.Reply("Only Teams channels have a Files tab.")
		return
	}
	channels, _ := client.channelTeams.Load().(map[string]graph.ChannelInfo)
	if channels == nil {
		channels = client.fetchTeamChannelMap(ce.Ctx)
		if channels != nil {
			client.channelTeams.Store(channels)
		}
	}
	info, ok := channels[channelID]
	if !ok || info.TeamID == "" {
		// Graph's list of joined teams leaves some out; the channel's own
		// thread names its team's group.
		var names []string
		if api, ok := client.getAPI().(threadPropertiesAPI); ok {
			if props, err := api.GetThreadProperties(ce.Ctx, channelID); err == nil {
				info.TeamID = props["groupId"]
				if info.ChannelName == "" {
					info.ChannelName = props["topic"]
				}
				for name := range props {
					names = append(names, name)
				}
				sort.Strings(names)
			} else {
				names = []string{"error: " + err.Error()}
			}
		}
		if info.TeamID == "" {
			ce.Reply("Teams files: this channel's team is not known (%d channels known from Graph; thread properties: %s).",
				len(channels), strings.Join(names, ", "))
			return
		}
	}
	if err := client.ensureValidGraphToken(ce.Ctx); err != nil {
		ce.Reply("Teams files failed: %s", err)
		return
	}
	gc, err := client.getGraphClient(ce.Ctx)
	if err != nil {
		ce.Reply("Teams files failed: %s", err)
		return
	}
	files, err := gc.ListChannelFiles(ce.Ctx, info.TeamID, channelID)
	if err != nil {
		ce.Reply("Teams files failed: %s", err)
		return
	}
	if files == nil {
		files = []graph.ChannelFile{}
	}
	dp := ce.User.DoublePuppet(ce.Ctx)
	asIntent, ok := dp.(*matrix.ASIntent)
	if !ok {
		ce.Reply("Teams files failed: no double puppet to write the room's account data")
		return
	}
	if err := asIntent.Matrix.SetRoomAccountData(ce.Ctx, portal.MXID, channelFilesAccountDataType, map[string]any{
		"files":      files,
		"fetched_ts": time.Now().UnixMilli(),
	}); err != nil {
		ce.Reply("Teams files failed: %s", err)
		return
	}
	ce.Reply("Teams files: %d in %s.", len(files), info.ChannelName)
}
