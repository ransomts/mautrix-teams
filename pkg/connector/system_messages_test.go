package connector

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func testNames(mri string) string {
	return map[string]string{
		"8:orgid:alice": "Alice Adams",
		"8:orgid:bob":   "Bob Brown",
		"8:orgid:carol": "Carol Cruz",
	}[mri]
}

func TestParseSystemMessage(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		raw         string
		wantOK      bool
		wantText    string
	}{
		{
			name:        "members added by a person",
			messageType: "ThreadActivity/AddMember",
			raw:         `<addmember><target>8:orgid:bob</target><target>8:orgid:carol</target><eventtime>1</eventtime><rosterVersion>2</rosterVersion><lastRosterVersion>1</lastRosterVersion><initiator>8:orgid:alice</initiator></addmember>`,
			wantOK:      true,
			wantText:    "Alice Adams added Bob Brown and Carol Cruz to the channel",
		},
		{
			name:        "members added by an app",
			messageType: "ThreadActivity/AddMember",
			raw:         `<addmember><target>8:orgid:bob</target><eventtime>1</eventtime><initiator>28:app:0c9bf8f6_db6f2704</initiator></addmember>`,
			wantOK:      true,
			wantText:    "Bob Brown was added to the channel",
		},
		{
			name:        "unknown member added",
			messageType: "ThreadActivity/AddMember",
			raw:         `<addmember><target>8:orgid:nobody</target><initiator>8:orgid:alice</initiator></addmember>`,
			wantOK:      true,
			wantText:    "Alice Adams added someone to the channel",
		},
		{
			name:        "member removed",
			messageType: "ThreadActivity/DeleteMember",
			raw:         `<deletemember><target>8:orgid:bob</target><initiator>8:orgid:alice</initiator></deletemember>`,
			wantOK:      true,
			wantText:    "Alice Adams removed Bob Brown from the channel",
		},
		{
			name:        "member left",
			messageType: "ThreadActivity/DeleteMember",
			raw:         `<deletemember><target>8:orgid:bob</target><initiator>8:orgid:bob</initiator></deletemember>`,
			wantOK:      true,
			wantText:    "Bob Brown left the channel",
		},
		{
			name:        "role update",
			messageType: "ThreadActivity/RoleUpdate",
			raw:         `<roleupdate><target><id>8:orgid:bob</id><role>admin</role></target><eventtime>1</eventtime><initiator>8:orgid:alice</initiator></roleupdate>`,
			wantOK:      true,
			wantText:    "Alice Adams made Bob Brown an owner",
		},
		{
			name:        "channel renamed (JSON)",
			messageType: "ThreadActivity/SpaceTopicUpdated",
			raw:         `{"oldValue":"TAs","newValue":"Teaching Team","user":"8:orgid:alice"}`,
			wantOK:      true,
			wantText:    "Alice Adams renamed the channel from “TAs” to “Teaching Team”",
		},
		{
			name:        "channel named by an app (JSON)",
			messageType: "ThreadActivity/SpaceTopicUpdated",
			raw:         `{"oldValue":null,"newValue":"CPSC 4300/6300","user":"28:app:0c9bf8f6_62b732f7"}`,
			wantOK:      true,
			wantText:    "The channel was renamed to “CPSC 4300/6300”",
		},
		{
			name:        "chat topic (XML)",
			messageType: "ThreadActivity/TopicUpdate",
			raw:         `<topicupdate><eventtime>1</eventtime><initiator>8:orgid:alice</initiator><value>Lunch plans</value></topicupdate>`,
			wantOK:      true,
			wantText:    "Alice Adams renamed the channel to “Lunch plans”",
		},
		{
			name:        "description changed (JSON)",
			messageType: "ThreadActivity/SpaceDescriptionUpdated",
			raw:         `{"oldValue":"CPSC 1050 F25","newValue":"CPSC 1050 SP26","user":"8:orgid:alice"}`,
			wantOK:      true,
			wantText:    "Alice Adams changed the description from “CPSC 1050 F25” to “CPSC 1050 SP26”",
		},
		{
			name:        "channels added (JSON lists)",
			messageType: "ThreadActivity/SpaceThreadChannelAdded",
			raw:         `{"oldValue":"[{\"id\":\"19:a@thread.tacv2\",\"name\":\"Lab Help\"}]","newValue":"[{\"id\":\"19:a@thread.tacv2\",\"name\":\"Lab Help\"},{\"id\":\"19:b@thread.tacv2\",\"name\":\"Office Hours\"}]","user":"8:orgid:alice"}`,
			wantOK:      true,
			wantText:    "Alice Adams added the channel “Office Hours”",
		},
		{
			name:        "no channel actually added",
			messageType: "ThreadActivity/SpaceThreadChannelAdded",
			raw:         `{"oldValue":"[{\"id\":\"19:a\",\"name\":\"Lab Help\"}]","newValue":"[{\"id\":\"19:a\",\"name\":\"Lab Help\"}]","user":"8:orgid:alice"}`,
			wantOK:      false,
		},
		{
			name:        "members joined with names from Teams",
			messageType: "ThreadActivity/MemberJoined",
			raw:         `{"eventtime":1,"initiator":"8:orgid:alice","members":[{"id":"8:orgid:shuwen","friendlyname":"Shuwen Wang"},{"id":"8:orgid:bob","friendlyname":"B."}]}`,
			wantOK:      true,
			wantText:    "Shuwen Wang and Bob Brown joined the channel",
		},
		{
			name:        "loop component added",
			messageType: "ThreadActivity/FileAdd",
			raw:         `<fileadd><eventtime>1</eventtime><initiator>8:orgid:alice</initiator><target><id>x</id><uri>https://example.invalid/loop</uri><type>loop</type><subtype>loopthread</subtype></target></fileadd>`,
			wantOK:      true,
			wantText:    "Alice Adams added a Loop component",
		},
		{
			name:        "favourite flag is bookkeeping",
			messageType: "ThreadActivity/UpdateFavDefault",
			raw:         `<UpdateFavDefault><eventtime>1</eventtime><initiator>8:orgid:alice</initiator><isDefault>True</isDefault></UpdateFavDefault>`,
			wantOK:      false,
		},
		{
			name:        "modality update is bookkeeping",
			messageType: "ThreadActivity/ModalityUpdate",
			raw:         `<modalityupdate><eventtime>1</eventtime><initiator>8:orgid:alice</initiator><value>PostReply</value></modalityupdate>`,
			wantOK:      false,
		},
		{
			name:        "channel list update is bookkeeping",
			messageType: "ThreadActivity/ChannelsUpdated",
			raw:         `{"oldValue":"[{\"id\":\"19:x@thread.tacv2\",\"name\":\"Off Topic\"}]","newValue":"[]"}`,
			wantOK:      false,
		},
		{
			name:        "not a system message",
			messageType: "RichText/Html",
			raw:         `<p>hello</p>`,
			wantOK:      false,
		},
		{
			name:        "malformed XML",
			messageType: "ThreadActivity/AddMember",
			raw:         `<addmember><target>8:orgid:bob`,
			wantOK:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := parseSystemMessage(tc.messageType, tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (activity %+v)", ok, tc.wantOK, a)
			}
			if !ok {
				return
			}
			got := systemMessageText(a, "the channel", testNames)
			if got != tc.wantText {
				t.Errorf("text = %q, want %q", got, tc.wantText)
			}
		})
	}
}

func TestJoinNames(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{nil, "nobody"},
		{[]string{"A"}, "A"},
		{[]string{"A", "B"}, "A and B"},
		{[]string{"A", "B", "C"}, "A, B and C"},
		{[]string{"A", "B", "C", "D", "E", "F", "G"}, "A, B, C, D, E, F and G"},
		{[]string{"A", "B", "C", "D", "E", "F", "G", "H"}, "A, B, C, D, E, F and 2 others"},
	}
	for _, tc := range tests {
		if got := joinNames(tc.names); got != tc.want {
			t.Errorf("joinNames(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}
}

func TestPlaceForThread(t *testing.T) {
	if got := placeForThread("19:abc@thread.tacv2"); got != "the channel" {
		t.Errorf("channel: %q", got)
	}
	if got := placeForThread("19:abc@thread.v2"); got != "the chat" {
		t.Errorf("chat: %q", got)
	}
}

func TestConvertTeamsMessageDeleted(t *testing.T) {
	c := &TeamsClient{}
	_, err := c.convertTeamsMessage(context.Background(), nil, nil, model.RemoteMessage{
		MessageID:     "1",
		MessageType:   "RichText/Html",
		PropertiesRaw: []byte(`{"deletetime":"1771359230016"}`),
	})
	if !errors.Is(err, errDeletedMessage) {
		t.Errorf("err = %v, want errDeletedMessage", err)
	}
}

func TestConvertTeamsMessageSafeLinkOnly(t *testing.T) {
	c := &TeamsClient{}
	cm, err := c.convertTeamsMessage(context.Background(), nil, nil, model.RemoteMessage{
		MessageID:     "1",
		MessageType:   "Text",
		PropertiesRaw: []byte(`{"files":"[]","atp":"[{\"URL\":\"https://example.sharepoint.com/personal/x/Documents/scan.pdf\"}]"}`),
	})
	if err != nil || cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("cm = %+v, err = %v", cm, err)
	}
	if body := cm.Parts[0].Content.Body; body != "Attachment: https://example.sharepoint.com/personal/x/Documents/scan.pdf" {
		t.Errorf("body = %q", body)
	}
}
