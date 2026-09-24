package connector

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// callSummaryKey is the event content key carrying a call's structured
// summary, so a client can act on it (notify on a missed call) without
// parsing the notice text.
const callSummaryKey = "fi.mau.teams.call"

// callSummary is what Teams records about a call: from an Event/Call
// message's XML in the chat, or from the call-log stream's call-log
// property.
type callSummary struct {
	State        string        // started, ended, missed, accepted, declined, ...
	Direction    string        // incoming or outgoing; empty when Teams does not say
	Peer         string        // the other party's name in a two-party call
	PeerID       string        // their Teams user ID
	Participants []string      // everyone else's names, for group calls
	Duration     time.Duration // zero when unknown or not connected
}

// callEventXML is the content of an Event/Call message: a state element
// (<started/>, <ended/>, <missed/>) and the participant list.
type callEventXML struct {
	Partlist struct {
		Type  string `xml:"type,attr"`
		Parts []struct {
			Identity    string `xml:"identity,attr"`
			DisplayName string `xml:"displayName"`
			Duration    string `xml:"duration"`
		} `xml:"part"`
	} `xml:"partlist"`
}

var callStates = []string{"missed", "started", "ended", "declined", "rejected"}

// parseCallEvent reads an Event/Call message's XML content.  selfID is left
// out of the participants.
func parseCallEvent(raw, selfID string) (*callSummary, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "<") {
		return nil, false
	}
	var doc callEventXML
	if err := xml.Unmarshal([]byte("<call>"+raw+"</call>"), &doc); err != nil {
		return nil, false
	}
	cs := &callSummary{State: strings.ToLower(strings.TrimSpace(doc.Partlist.Type))}
	if cs.State == "" {
		for _, state := range callStates {
			if strings.Contains(raw, "<"+state+"/>") || strings.Contains(raw, "<"+state+">") {
				cs.State = state
				break
			}
		}
	}
	if cs.State == "" {
		return nil, false
	}
	self := model.NormalizeTeamsUserID(selfID)
	for _, part := range doc.Partlist.Parts {
		if secs, err := strconv.Atoi(strings.TrimSpace(part.Duration)); err == nil {
			cs.Duration = max(cs.Duration, time.Duration(secs)*time.Second)
		}
		id := model.NormalizeTeamsUserID(part.Identity)
		name := strings.TrimSpace(part.DisplayName)
		if (self != "" && id == self) || name == "" {
			continue // the user, or a bot/recorder with no name
		}
		cs.Participants = append(cs.Participants, name)
		if cs.PeerID == "" {
			cs.PeerID = id
		}
	}
	if len(cs.Participants) == 1 {
		cs.Peer = cs.Participants[0]
	}
	return cs, true
}

// callLogEntry is the call-log property of a call-log stream message.
type callLogEntry struct {
	ConnectTime           *time.Time `json:"connectTime"`
	EndTime               *time.Time `json:"endTime"`
	CallDirection         string     `json:"callDirection"`
	CallType              string     `json:"callType"`
	CallState             string     `json:"callState"`
	OriginatorParticipant struct {
		ID          string  `json:"id"`
		DisplayName *string `json:"displayName"`
	} `json:"originatorParticipant"`
	TargetParticipant struct {
		ID          string  `json:"id"`
		DisplayName *string `json:"displayName"`
	} `json:"targetParticipant"`
}

// parseCallLog reads the call-log property from a message's properties.
// Teams sends it as a JSON string holding a JSON object; an object is
// accepted too.
func parseCallLog(props json.RawMessage) (*callSummary, bool) {
	if len(props) == 0 {
		return nil, false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(props, &payload); err != nil {
		return nil, false
	}
	raw, ok := payload["call-log"]
	if !ok {
		return nil, false
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err == nil {
		raw = json.RawMessage(inner)
	}
	var entry callLogEntry
	if err := json.Unmarshal(raw, &entry); err != nil || entry.CallState == "" {
		return nil, false
	}
	cs := &callSummary{
		State:     strings.ToLower(entry.CallState),
		Direction: strings.ToLower(entry.CallDirection),
	}
	peer := entry.TargetParticipant
	if cs.Direction == "incoming" {
		peer = entry.OriginatorParticipant
	}
	cs.PeerID = model.NormalizeTeamsUserID(peer.ID)
	if peer.DisplayName != nil {
		cs.Peer = strings.TrimSpace(*peer.DisplayName)
	}
	if cs.Peer != "" {
		cs.Participants = []string{cs.Peer}
	}
	if entry.ConnectTime != nil && entry.EndTime != nil && entry.EndTime.After(*entry.ConnectTime) {
		cs.Duration = entry.EndTime.Sub(*entry.ConnectTime).Round(time.Second)
	}
	return cs, true
}

// formatCallDuration renders d as "45 s", "12 min" or "1 h 5 min".
func formatCallDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
	default:
		h := int(d.Hours())
		return fmt.Sprintf("%d h %d min", h, int(d.Round(time.Minute).Minutes())-60*h)
	}
}

// text is the notice for the call, e.g. "Missed call from Ann",
// "Outgoing call to Bob, 4 min" or "Call ended, 12 min: Ann, Bob".
func (cs *callSummary) text() string {
	who := cs.Peer
	if who == "" {
		who = "someone"
	}
	withDuration := func(s string) string {
		if cs.Duration > 0 {
			return s + ", " + formatCallDuration(cs.Duration)
		}
		return s
	}
	switch cs.State {
	case "missed":
		if cs.Direction == "outgoing" {
			return "Call to " + who + ", not answered"
		}
		return "Missed call from " + who
	case "declined", "rejected":
		if cs.Direction == "outgoing" {
			return "Call to " + who + " declined"
		}
		return "Declined call from " + who
	case "accepted":
		switch cs.Direction {
		case "incoming":
			return withDuration("Incoming call from " + who)
		case "outgoing":
			return withDuration("Outgoing call to " + who)
		}
		return withDuration("Call with " + who)
	case "started":
		return "Call started"
	case "ended":
		s := withDuration("Call ended")
		if len(cs.Participants) > 0 {
			s += ": " + strings.Join(cs.Participants, ", ")
		}
		return s
	}
	if cs.Direction == "outgoing" {
		return withDuration("Call to " + who + " (" + cs.State + ")")
	}
	return withDuration("Call with " + who + " (" + cs.State + ")")
}

// convertCall converts a Teams call record (an Event/Call message or a
// call-log stream entry) to a notice with its structured summary, or
// returns nil if msg is neither or cannot be read.
func (c *TeamsClient) convertCall(msg model.RemoteMessage) *bridgev2.ConvertedMessage {
	var cs *callSummary
	if strings.HasPrefix(strings.ToLower(msg.MessageType), "event/call") {
		cs, _ = parseCallEvent(msg.RawContent, c.selfTeamsUserID())
		if cs != nil && cs.State == "missed" && cs.Peer == "" {
			cs.Peer = strings.TrimSpace(msg.SenderName)
		}
	}
	if cs == nil {
		cs, _ = parseCallLog(msg.PropertiesRaw)
	}
	if cs == nil {
		return nil
	}
	extra := perMessageExtra(msg)
	if extra == nil {
		extra = map[string]any{}
	}
	summary := map[string]any{
		"state":            cs.State,
		"direction":        cs.Direction,
		"peer":             cs.Peer,
		"peer_id":          cs.PeerID,
		"participants":     cs.Participants,
		"duration_seconds": int(cs.Duration.Seconds()),
	}
	extra[callSummaryKey] = summary
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: cs.text()},
			Extra:   extra,
		}},
	}
}
