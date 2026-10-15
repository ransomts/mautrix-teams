package connector

// Teams' activity feed -> Matrix account data.  Teams keeps the user's
// Activity (reactions to their messages, replies to their posts, mentions)
// in the chat stream 48:notifications, one message per item with the
// details in properties.activity.  The newest items, mapped to the bridged
// room and event where there is one, are published as the account data
// event fi.mau.teams.activity through the double puppet whenever the
// newest changes, so a client sees activity that happened while it was not
// running.

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	activityStreamID        = "48:notifications"
	activityAccountDataType = "fi.mau.teams.activity"
	activityFeedSize        = 50
)

// activityItem is one published activity entry.
type activityItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	TS       int64  `json:"ts"`
	Who      string `json:"who"`
	WhoID    string `json:"who_id,omitempty"`
	Preview  string `json:"preview,omitempty"`
	Topic    string `json:"topic,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	RoomID   string `json:"room_id,omitempty"`
	EventID  string `json:"event_id,omitempty"`
}

// parseActivity reads properties.activity of an activity stream message.
func parseActivity(properties json.RawMessage) (map[string]any, bool) {
	var props struct {
		Activity json.RawMessage `json:"activity"`
	}
	if len(properties) == 0 || json.Unmarshal(properties, &props) != nil || len(props.Activity) == 0 {
		return nil, false
	}
	raw := []byte(props.Activity)
	// Sometimes a JSON string holding the object.
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	// Numbers as json.Number: message IDs are numbers here, and must stay exact.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var activity map[string]any
	if err := dec.Decode(&activity); err != nil {
		return nil, false
	}
	return activity, true
}

// activityString is the first of keys present in activity, as a string
// (numbers included).
func activityString(activity map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := activity[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case json.Number:
			return v.String()
		}
	}
	return ""
}

// pollActivityFeed reads the newest activity and publishes it when it changed.
func (c *TeamsClient) pollActivityFeed(ctx context.Context) {
	if c == nil || c.Main == nil || c.Main.Bridge == nil || c.Login == nil {
		return
	}
	msgs, err := c.getAPI().ListMessagesPaginated(ctx, activityStreamID, activityFeedSize, "")
	if err != nil {
		log := c.log()
		log.Debug().Err(err).Msg("Failed to read the Teams activity feed")
		return
	}
	portalKeyReceiver := c.portalKey("").Receiver
	items := make([]activityItem, 0, len(msgs))
	types := map[string]int{}
	for _, msg := range msgs {
		activity, ok := parseActivity(msg.PropertiesRaw)
		if !ok {
			continue
		}
		item := activityItem{
			ID:       msg.MessageID,
			Type:     activityString(activity, "activityType"),
			TS:       msg.Timestamp.UnixMilli(),
			Who:      activityString(activity, "sourceUserImDisplayName", "sourceUserDisplayName"),
			WhoID:    activityString(activity, "sourceUserId"),
			Preview:  activityString(activity, "messagePreview"),
			Topic:    activityString(activity, "sourceThreadTopic"),
			ThreadID: activityString(activity, "sourceThreadId"),
		}
		if ts, err := time.Parse(time.RFC3339, activityString(activity, "activityTimestamp")); err == nil {
			item.TS = ts.UnixMilli()
		}
		types[item.Type]++
		if item.ThreadID != "" {
			portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, c.portalKey(item.ThreadID))
			if err == nil && portal != nil && portal.MXID != "" {
				item.RoomID = string(portal.MXID)
				if msgID := activityString(activity, "sourceMessageId"); msgID != "" {
					part, err := c.Main.Bridge.DB.Message.GetFirstPartByID(ctx, portalKeyReceiver, networkid.MessageID(msgID))
					if err == nil && part != nil {
						item.EventID = string(part.MXID)
					}
				}
			}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].TS > items[j].TS })
	sig := items[0].ID + "/" + items[0].EventID
	if sig == c.activitySig {
		return
	}
	if c.activitySig == "" {
		log := c.log()
		log.Info().Int("items", len(items)).Interface("types", types).Msg("Teams activity feed read")
	}
	if err := c.publishAccountData(ctx, activityAccountDataType, map[string]any{
		"items":      items,
		"updated_ts": time.Now().UnixMilli(),
	}); err != nil {
		log := c.log()
		log.Debug().Err(err).Msg("Failed to publish the Teams activity feed")
		return
	}
	c.activitySig = sig
}

// publishAccountData sets an account data event of the login's Matrix user
// through the double puppet.
func (c *TeamsClient) publishAccountData(ctx context.Context, eventType string, content any) error {
	if c.Login == nil || c.Login.User == nil {
		return nil
	}
	dp := c.Login.User.DoublePuppet(ctx)
	if dp == nil {
		return nil
	}
	asIntent, ok := dp.(*matrix.ASIntent)
	if !ok {
		return nil
	}
	return asIntent.Matrix.SetAccountData(ctx, eventType, content)
}
