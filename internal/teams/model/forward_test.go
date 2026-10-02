package model

import (
	"testing"
	"time"
)

func TestExtractForwardContext(t *testing.T) {
	// As Teams sent it (2026-09).
	props := []byte(`{"forwardTemplateId":"basic_forward_message_template","originalMessageContext":{"originalSender":"8:orgid:c61ce4d2","originalSentTime":"2026-09-10T15:55:56.042Z","messageId":1789055756042,"originalThreadId":"19:28c8b426@thread.v2","threadType":"chat","messageType":"RichText/Html"}}`)
	got, ok := ExtractForwardContext(props)
	if !ok {
		t.Fatal("not recognised as forwarded")
	}
	want := ForwardContext{
		OriginalSender:   "8:orgid:c61ce4d2",
		OriginalSentTime: time.Date(2026, 9, 10, 15, 55, 56, 42000000, time.UTC),
		OriginalThreadID: "19:28c8b426@thread.v2",
	}
	if got.OriginalSender != want.OriginalSender || got.OriginalThreadID != want.OriginalThreadID || !got.OriginalSentTime.Equal(want.OriginalSentTime) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// The same, as a JSON string.
	if _, ok := ExtractForwardContext([]byte(`{"originalMessageContext":"{\"originalSender\":\"8:orgid:a\"}"}`)); !ok {
		t.Error("string-encoded context not recognised")
	}
	for _, props := range []string{``, `{}`, `{"originalMessageContext":{}}`, `nope`} {
		if _, ok := ExtractForwardContext([]byte(props)); ok {
			t.Errorf("%q recognised as forwarded", props)
		}
	}
}
