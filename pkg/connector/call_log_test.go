package connector

import (
	"encoding/json"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

const testSelf = "8:orgid:00000000-0000-0000-0000-000000000000"

// An Event/Call message as Teams sent it for a scheduled meeting (trimmed).
const endedMeetingXML = `<ended/><partlist alt="" count="3">` +
	`<part identity="8:orgid:00000001"><name>8:orgid:00000001</name><displayName>Sam Example</displayName><duration>712</duration></part>` +
	`<part identity="28:00000009"><name>28:00000009</name><displayName></displayName><duration>712</duration></part>` +
	`<part identity="` + testSelf + `"><name>` + testSelf + `</name><displayName>Me Myself</displayName><duration>712</duration></part>` +
	`</partlist><meetingDetails><meetingDetails><meetingType>Scheduled</meetingType></meetingDetails></meetingDetails>`

func callLogProps(t *testing.T, entry string) json.RawMessage {
	t.Helper()
	// Teams sends the call-log property as a string holding JSON.
	props, err := json.Marshal(map[string]any{"call-log": entry, "s2spartnername": "x"})
	if err != nil {
		t.Fatal(err)
	}
	return props
}

func TestParseCallEvent(t *testing.T) {
	cs, ok := parseCallEvent(endedMeetingXML, testSelf)
	if !ok {
		t.Fatal("not parsed")
	}
	if cs.State != "ended" || cs.Duration != 712*time.Second {
		t.Fatalf("state %q duration %v", cs.State, cs.Duration)
	}
	// The user and the nameless bot are left out.
	if len(cs.Participants) != 1 || cs.Peer != "Sam Example" {
		t.Fatalf("participants %v peer %q", cs.Participants, cs.Peer)
	}
	if got := cs.text(); got != "Call ended, 12 min: Sam Example" {
		t.Fatalf("text %q", got)
	}

	missed, ok := parseCallEvent(`<partlist type="missed" alt=""><part identity="8:orgid:ann"><displayName>Ann Lee</displayName></part></partlist>`, testSelf)
	if !ok || missed.State != "missed" || missed.text() != "Missed call from Ann Lee" {
		t.Fatalf("missed: %+v %q", missed, missed.text())
	}
	if _, ok := parseCallEvent("Call Logs for Call 123", testSelf); ok {
		t.Fatal("plain text parsed as a call event")
	}
	if _, ok := parseCallEvent("<p>hello</p>", testSelf); ok {
		t.Fatal("HTML without a call state parsed as a call event")
	}
}

func TestParseCallLog(t *testing.T) {
	missed := `{"startTime":"2026-04-06T16:53:51.58Z","connectTime":null,"endTime":"2026-04-06T16:54:03.05Z",` +
		`"callDirection":"incoming","callType":"twoParty","callState":"missed",` +
		`"originatorParticipant":{"id":"8:orgid:00000002","displayName":"Casey Example"},` +
		`"targetParticipant":{"id":"` + testSelf + `","displayName":null}}`
	cs, ok := parseCallLog(callLogProps(t, missed))
	if !ok || cs.text() != "Missed call from Casey Example" || cs.PeerID != "8:orgid:00000002" || cs.Duration != 0 {
		t.Fatalf("missed: %+v %q", cs, cs.text())
	}

	outgoing := `{"connectTime":"2026-05-06T17:35:48.09Z","endTime":"2026-05-06T17:39:50.50Z",` +
		`"callDirection":"outgoing","callType":"twoParty","callState":"accepted",` +
		`"originatorParticipant":{"id":"` + testSelf + `","displayName":"Me Myself"},` +
		`"targetParticipant":{"id":"8:orgid:00000003","displayName":null}}`
	cs, ok = parseCallLog(callLogProps(t, outgoing))
	if !ok || cs.text() != "Outgoing call to someone, 4 min" || cs.PeerID != "8:orgid:00000003" {
		t.Fatalf("outgoing: %+v %q", cs, cs.text())
	}

	if _, ok := parseCallLog(json.RawMessage(`{"s2spartnername":"x"}`)); ok {
		t.Fatal("parsed without a call-log property")
	}
}

func TestConvertCall(t *testing.T) {
	var c *TeamsClient
	cm := c.convertCall(model.RemoteMessage{MessageType: "Event/Call", RawContent: endedMeetingXML})
	if cm == nil || cm.Parts[0].Content.MsgType != event.MsgNotice {
		t.Fatalf("event/call not converted: %+v", cm)
	}
	summary, ok := cm.Parts[0].Extra[callSummaryKey].(map[string]any)
	if !ok || summary["state"] != "ended" || summary["duration_seconds"] != 712 {
		t.Fatalf("summary %+v", cm.Parts[0].Extra)
	}

	// An ordinary message is left to the rest of the converter.
	if c.convertCall(model.RemoteMessage{MessageType: "RichText/Html", Body: "hi"}) != nil {
		t.Fatal("ordinary message converted as a call")
	}
	// Unreadable call XML falls through to the generic converter.
	if c.convertCall(model.RemoteMessage{MessageType: "Event/Call"}) != nil {
		t.Fatal("empty Event/Call converted")
	}
}

func TestFormatCallDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:  "45 s",
		712 * time.Second: "12 min",
		time.Hour + 5*time.Minute + 20*time.Second: "1 h 5 min",
	} {
		if got := formatCallDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}
