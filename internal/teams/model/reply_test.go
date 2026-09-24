package model

import (
	"encoding/json"
	"testing"
)

func TestExtractThreadRootID(t *testing.T) {
	props := json.RawMessage(`{"replyChainMessageId": "1234567890"}`)
	id := ExtractThreadRootID(props)
	if id != "1234567890" {
		t.Errorf("expected 1234567890, got %s", id)
	}
}

func TestExtractThreadRootID_Empty(t *testing.T) {
	props := json.RawMessage(`{}`)
	id := ExtractThreadRootID(props)
	if id != "" {
		t.Errorf("expected empty string, got %s", id)
	}
}

func TestExtractThreadRootID_Nil(t *testing.T) {
	id := ExtractThreadRootID(nil)
	if id != "" {
		t.Errorf("expected empty string, got %s", id)
	}
}

func TestResolveThreadRootID(t *testing.T) {
	const link = "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/19:62bf@thread.tacv2"
	cases := []struct {
		name, link string
		props      json.RawMessage
		want       string
	}{
		{"channel reply", link + ";messageid=1768577863084", nil, "1768577863084"},
		{"link wins over property", link + ";messageid=111", json.RawMessage(`{"replyChainMessageId":"222"}`), "111"},
		{"property fallback", link, json.RawMessage(`{"replyChainMessageId":"222"}`), "222"},
		{"chat message", "https://x/v1/users/ME/conversations/19:a_b@unq.gbl.spaces", nil, ""},
		{"nothing", "", nil, ""},
	}
	for _, tc := range cases {
		if got := ResolveThreadRootID(tc.link, tc.props); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
