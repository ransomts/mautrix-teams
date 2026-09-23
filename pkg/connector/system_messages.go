package connector

// Teams system messages.  Membership, role and name changes are posted into
// a conversation by Teams itself, from the conversation's own thread ID,
// as ThreadActivity/* messages whose content is XML or JSON.  They are
// shown as notices from one "Teams" ghost, in the third person, with the
// people named through the profile table and Graph.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
	"go.mau.fi/mautrix-teams/pkg/teamsdb"
)

// systemSenderID is the network user ID of the ghost that posts system
// notices in every room.  It is not a Teams MRI, so nothing mistakes it for
// a person: name lookups, presence and outbound membership all key on "8:"
// user IDs.
const systemSenderID = "teams:system"

// systemSenderName is that ghost's display name.
const systemSenderName = "Teams"

func systemEventSender() bridgev2.EventSender {
	return bridgev2.EventSender{Sender: networkid.UserID(systemSenderID)}
}

// systemActivity is what a ThreadActivity message records.
type systemActivity struct {
	Kind      string            // "added", "removed", "joined", "role", "renamed", "described", "channels" or "file"
	Initiator string            // MRI of who did it: a person ("8:…"), an app ("28:app:…") or empty
	Targets   []string          // MRIs of who it was done to; channel names for "channels"
	Names     map[string]string // names Teams gave for Targets, when it did
	Role      string            // "admin" or "user", for Kind "role"
	OldValue  string            // for "renamed" and "described"; may be empty
	NewValue  string            // for "renamed" and "described"; the file kind for "file"
}

type xmlActivity struct {
	Initiator string      `xml:"initiator"`
	Value     string      `xml:"value"`
	Targets   []xmlTarget `xml:"target"`
}

// xmlTarget is <target>MRI</target>, <target><id>MRI</id><role>admin</role></target>
// or, for a file, <target><id>…</id><type>loop</type><subtype>…</subtype></target>.
type xmlTarget struct {
	Text string `xml:",chardata"`
	ID   string `xml:"id"`
	Role string `xml:"role"`
	Type string `xml:"type"`
}

// jsonValueChange is the JSON body of the Space* activities: a value that
// changed and who changed it.
type jsonValueChange struct {
	OldValue string `json:"oldValue"`
	NewValue string `json:"newValue"`
	User     string `json:"user"`
}

// parseSystemMessage reads a ThreadActivity message's raw content.  Only
// the activities worth a line in the room parse: the rest (read modality,
// favourites, tabs, channel-list bookkeeping) return false and stay out.
func parseSystemMessage(messageType, raw string) (*systemActivity, bool) {
	kind := strings.ToLower(strings.TrimSpace(messageType))
	kind, ok := strings.CutPrefix(kind, "threadactivity/")
	if !ok {
		return nil, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	switch kind {
	case "addmember", "deletemember", "roleupdate", "topicupdate", "fileadd":
		var x xmlActivity
		if err := xml.Unmarshal([]byte(raw), &x); err != nil {
			return nil, false
		}
		a := &systemActivity{Initiator: model.NormalizeTeamsUserID(x.Initiator)}
		for _, t := range x.Targets {
			mri := model.NormalizeTeamsUserID(t.ID)
			if mri == "" {
				mri = model.NormalizeTeamsUserID(t.Text)
			}
			if mri == "" {
				continue
			}
			a.Targets = append(a.Targets, mri)
			if role := strings.TrimSpace(t.Role); role != "" {
				a.Role = strings.ToLower(role)
			}
		}
		switch kind {
		case "addmember":
			a.Kind = "added"
		case "deletemember":
			a.Kind = "removed"
		case "roleupdate":
			a.Kind = "role"
		case "topicupdate":
			a.Kind = "renamed"
			a.NewValue = strings.TrimSpace(x.Value)
		case "fileadd":
			a.Kind = "file"
			a.Targets = nil
			if len(x.Targets) > 0 {
				a.NewValue = strings.ToLower(strings.TrimSpace(x.Targets[0].Type))
			}
		}
		switch a.Kind {
		case "renamed":
			if a.NewValue == "" {
				return nil, false
			}
		case "file":
		default:
			if len(a.Targets) == 0 {
				return nil, false
			}
		}
		return a, true
	case "spacetopicupdated", "spacedescriptionupdated":
		var j jsonValueChange
		if err := json.Unmarshal([]byte(raw), &j); err != nil || strings.TrimSpace(j.NewValue) == "" {
			return nil, false
		}
		a := &systemActivity{
			Kind:      "renamed",
			Initiator: model.NormalizeTeamsUserID(j.User),
			OldValue:  strings.TrimSpace(j.OldValue),
			NewValue:  strings.TrimSpace(j.NewValue),
		}
		if kind == "spacedescriptionupdated" {
			a.Kind = "described"
		}
		return a, true
	case "spacethreadchanneladded":
		// Old and new channel lists, each a JSON array in a string.
		var j jsonValueChange
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, false
		}
		names := func(list string) map[string]bool {
			var channels []struct {
				Name string `json:"name"`
			}
			out := make(map[string]bool)
			if err := json.Unmarshal([]byte(list), &channels); err == nil {
				for _, ch := range channels {
					if name := strings.TrimSpace(ch.Name); name != "" {
						out[name] = true
					}
				}
			}
			return out
		}
		old := names(j.OldValue)
		a := &systemActivity{Kind: "channels", Initiator: model.NormalizeTeamsUserID(j.User)}
		for name := range names(j.NewValue) {
			if !old[name] {
				a.Targets = append(a.Targets, name)
			}
		}
		if len(a.Targets) == 0 {
			return nil, false
		}
		sort.Strings(a.Targets)
		return a, true
	case "memberjoined":
		var j struct {
			Initiator string `json:"initiator"`
			Members   []struct {
				ID   string `json:"id"`
				Name string `json:"friendlyname"`
			} `json:"members"`
		}
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, false
		}
		a := &systemActivity{Kind: "joined", Initiator: model.NormalizeTeamsUserID(j.Initiator), Names: make(map[string]string)}
		for _, m := range j.Members {
			mri := model.NormalizeTeamsUserID(m.ID)
			if mri == "" {
				continue
			}
			a.Targets = append(a.Targets, mri)
			if name := strings.TrimSpace(m.Name); name != "" {
				a.Names[mri] = name
			}
		}
		if len(a.Targets) == 0 {
			return nil, false
		}
		return a, true
	}
	return nil, false
}

// nameResolver names a Teams user for a notice; "" when it cannot.
type nameResolver func(mri string) string

// isPersonMRI reports whether an MRI is a user rather than an app or a
// conversation.
func isPersonMRI(mri string) bool {
	return strings.HasPrefix(mri, "8:")
}

// systemMessageText says what a did, in the third person.  place is "the
// channel" or "the chat".  An initiator that is an app (a class roster
// sync, say) or unknown gets a passive sentence.
func systemMessageText(a *systemActivity, place string, names nameResolver) string {
	who := func(mri string) string {
		if names != nil {
			if n := strings.TrimSpace(names(mri)); n != "" {
				return n
			}
		}
		if n := strings.TrimSpace(a.Names[mri]); n != "" {
			return n
		}
		return "someone"
	}
	actor := ""
	if isPersonMRI(a.Initiator) {
		actor = who(a.Initiator)
	}
	people := make([]string, 0, len(a.Targets))
	for _, mri := range a.Targets {
		people = append(people, who(mri))
	}
	plural := len(a.Targets) > 1
	list := joinNames(people)
	were := "was"
	are := "is"
	if plural {
		were = "were"
		are = "are"
	}

	switch a.Kind {
	case "added":
		if actor != "" {
			return fmt.Sprintf("%s added %s to %s", actor, list, place)
		}
		return fmt.Sprintf("%s %s added to %s", list, were, place)
	case "removed":
		if actor != "" && len(a.Targets) == 1 && a.Targets[0] == a.Initiator {
			return fmt.Sprintf("%s left %s", actor, place)
		}
		if actor != "" {
			return fmt.Sprintf("%s removed %s from %s", actor, list, place)
		}
		return fmt.Sprintf("%s %s removed from %s", list, were, place)
	case "role":
		var role string
		switch a.Role {
		case "admin":
			role = "an owner"
			if plural {
				role = "owners"
			}
		case "user":
			role = "a member"
			if plural {
				role = "members"
			}
		default:
			role = a.Role
		}
		if actor != "" {
			return fmt.Sprintf("%s made %s %s", actor, list, role)
		}
		return fmt.Sprintf("%s %s now %s", list, are, role)
	case "joined":
		return fmt.Sprintf("%s joined %s", list, place)
	case "renamed":
		from := ""
		if a.OldValue != "" {
			from = fmt.Sprintf(" from “%s”", a.OldValue)
		}
		if actor != "" {
			return fmt.Sprintf("%s renamed %s%s to “%s”", actor, place, from, a.NewValue)
		}
		return fmt.Sprintf("%s was renamed%s to “%s”", capitalizeFirst(place), from, a.NewValue)
	case "described":
		from := ""
		if a.OldValue != "" {
			from = fmt.Sprintf(" from “%s”", a.OldValue)
		}
		if actor != "" {
			return fmt.Sprintf("%s changed the description%s to “%s”", actor, from, a.NewValue)
		}
		return fmt.Sprintf("The description was changed%s to “%s”", from, a.NewValue)
	case "channels":
		quoted := make([]string, 0, len(a.Targets))
		for _, name := range a.Targets {
			quoted = append(quoted, fmt.Sprintf("“%s”", name))
		}
		noun := "the channel"
		if plural {
			noun = "the channels"
		}
		if actor != "" {
			return fmt.Sprintf("%s added %s %s", actor, noun, joinNames(quoted))
		}
		return fmt.Sprintf("%s %s %s added", capitalizeFirst(noun), joinNames(quoted), were)
	case "file":
		thing := "a file"
		if a.NewValue == "loop" {
			thing = "a Loop component"
		}
		if actor != "" {
			return fmt.Sprintf("%s added %s", actor, thing)
		}
		return fmt.Sprintf("%s was added", capitalizeFirst(thing))
	}
	return ""
}

// joinNames lists names in prose: "A", "A and B", "A, B and C", and past
// six, "A, B, C, D, E, F and 12 others".
func joinNames(names []string) string {
	const shown = 6
	switch len(names) {
	case 0:
		return "nobody"
	case 1:
		return names[0]
	}
	if len(names) > shown+1 {
		return fmt.Sprintf("%s and %d others", strings.Join(names[:shown], ", "), len(names)-shown)
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// placeForThread is what a notice calls the conversation a thread ID names.
func placeForThread(threadID string) string {
	if strings.Contains(strings.ToLower(threadID), "@thread.tacv2") {
		return "the channel"
	}
	return "the chat"
}

// systemMessageEvent is the remote event for a Teams system message in th,
// or nil when the message is not one worth a line.
func (c *TeamsClient) systemMessageEvent(th *teamsdb.ThreadState, msg model.RemoteMessage) *simplevent.Message[model.RemoteMessage] {
	if _, ok := parseSystemMessage(msg.MessageType, msg.RawContent); !ok {
		return nil
	}
	return &simplevent.Message[model.RemoteMessage]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    c.portalKey(th.ThreadID),
			Sender:       systemEventSender(),
			CreatePortal: true,
			Timestamp:    msg.Timestamp,
			StreamOrder:  msg.Timestamp.UnixMilli(),
		},
		Data:               msg,
		ID:                 networkid.MessageID(strings.TrimSpace(msg.MessageID)),
		ConvertMessageFunc: c.convertSystemMessage,
	}
}

// convertSystemMessage renders a Teams system message as a notice.
func (c *TeamsClient) convertSystemMessage(ctx context.Context, portal *bridgev2.Portal, _ bridgev2.MatrixAPI, msg model.RemoteMessage) (*bridgev2.ConvertedMessage, error) {
	a, ok := parseSystemMessage(msg.MessageType, msg.RawContent)
	if !ok {
		return nil, fmt.Errorf("not a renderable system message: %s", msg.MessageType)
	}
	threadID := ""
	if portal != nil {
		threadID = string(portal.ID)
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: c.systemNoticeContent(ctx, a, threadID),
		}},
	}, nil
}

func (c *TeamsClient) systemNoticeContent(ctx context.Context, a *systemActivity, threadID string) *event.MessageEventContent {
	text := systemMessageText(a, placeForThread(threadID), func(mri string) string {
		return c.resolveUserDisplayName(ctx, mri)
	})
	return &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    text,
	}
}
