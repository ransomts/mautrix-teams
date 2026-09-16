package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Matrix HTML message (e.g. ement's Org filter output) must reach Teams as
// real HTML, not with the tags escaped into visible text.
func TestSendFormattedMessageDoesNotEscapeHTML(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"OriginalArrivalTime":1}`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.SendMessagesURL = server.URL + "/conversations"
	client.Token = "t"

	_, err := client.SendFormattedMessage(context.Background(), "19:abc@thread.v2",
		"<p>\nsounds good, thanks!</p>", "8:live:me", GenerateClientMessageID(), "", nil)
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if payload["content"] != "<p>\nsounds good, thanks!</p>" {
		t.Fatalf("HTML must be sent verbatim, got %q", payload["content"])
	}
	if payload["messagetype"] != "RichText/Html" {
		t.Fatalf("unexpected messagetype: %q", payload["messagetype"])
	}
}
