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
