package connector

// The directory job title and department of each DM's other member, sent to
// the room as a fi.mau.teams.directory state event, so a client can group
// DMs (faculty and staff apart from students) without directory access of
// its own.  The event's presence means the lookup succeeded: empty fields
// are what the directory says, not a failure.

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

var directoryInfoEventType = event.Type{Type: "fi.mau.teams.directory", Class: event.StateEventType}

// directoryRecheckInterval is how long a DM counterpart's directory entry is
// trusted before it is looked up again.
const directoryRecheckInterval = 24 * time.Hour

// syncDMDirectoryInfo looks up every DM counterpart not checked within
// directoryRecheckInterval and sends the room a new directory event when the
// title or department changed.
func (c *TeamsClient) syncDMDirectoryInfo(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.DB == nil || c.Main.Bridge == nil || c.Login == nil {
		return
	}
	log := zerolog.Ctx(ctx)
	threads, err := c.Main.DB.ThreadState.ListForLogin(ctx, c.Login.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list threads for directory info")
		return
	}
	var gc *graph.GraphClient
	now := time.Now()
	for _, th := range threads {
		if ctx.Err() != nil || c.superseded() {
			return
		}
		if th == nil || !th.IsOneToOne {
			continue
		}
		uuid, ok := strings.CutPrefix(c.dmCounterpartFromThreadID(th.ThreadID), "8:orgid:")
		if !ok || uuid == "" {
			continue
		}
		portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(th.ThreadID))
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		meta, _ := portal.Metadata.(*teamsid.PortalMetadata)
		if meta == nil {
			continue
		}
		prev := meta.Directory
		if prev != nil && now.Sub(time.UnixMilli(prev.CheckedAt)) < directoryRecheckInterval {
			continue
		}
		if gc == nil {
			if err := c.ensureValidGraphToken(ctx); err != nil {
				return
			}
			if gc, err = c.getGraphClient(ctx); err != nil {
				return
			}
		}
		user, err := gc.GetUserByEmail(ctx, uuid) // accepts an object ID too
		if err != nil {
			log.Debug().Err(err).Str("thread_id", th.ThreadID).Msg("Directory lookup failed")
			continue
		}
		if user == nil {
			continue // a deleted account
		}
		info := &teamsid.DirectoryInfo{
			JobTitle:   strings.TrimSpace(user.JobTitle),
			Department: strings.TrimSpace(user.Department),
			CheckedAt:  now.UnixMilli(),
		}
		if prev == nil || prev.JobTitle != info.JobTitle || prev.Department != info.Department {
			content := &event.Content{Raw: map[string]any{
				"job_title":  info.JobTitle,
				"department": info.Department,
			}}
			if _, err := c.Main.Bridge.Bot.SendState(ctx, portal.MXID, directoryInfoEventType, "", content, time.Time{}); err != nil {
				log.Warn().Err(err).Str("thread_id", th.ThreadID).Msg("Failed to send directory info")
				continue
			}
		}
		meta.Directory = info
		if err := portal.Save(ctx); err != nil {
			log.Warn().Err(err).Str("thread_id", th.ThreadID).Msg("Failed to save directory info")
		}
	}
}
