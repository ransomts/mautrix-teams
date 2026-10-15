package connector

import (
	"encoding/json"
	"testing"
)

func TestParseActivity(t *testing.T) {
	obj := json.RawMessage(`{"activity":{"activityType":"reactionInChat","sourceUserImDisplayName":"Alice","sourceThreadId":"19:a@thread.v2","sourceMessageId":1790600560290123}}`)
	act, ok := parseActivity(obj)
	if !ok || activityString(act, "activityType") != "reactionInChat" || activityString(act, "sourceUserDisplayName", "sourceUserImDisplayName") != "Alice" {
		t.Errorf("object: %v %v", act, ok)
	}
	if got := activityString(act, "sourceMessageId"); got != "1790600560290123" {
		t.Errorf("numeric ID = %q", got)
	}
	str := json.RawMessage(`{"activity":"{\"activityType\":\"reply\"}"}`)
	if act, ok := parseActivity(str); !ok || activityString(act, "activityType") != "reply" {
		t.Errorf("string: %v %v", act, ok)
	}
	if _, ok := parseActivity(json.RawMessage(`{"other":1}`)); ok {
		t.Error("no activity parsed as one")
	}
}
