package connector

import (
	"encoding/json"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// Link previews land on the last text part only, with empty fields left out.
func TestApplyLinkPreviews(t *testing.T) {
	parts := []*bridgev2.ConvertedMessagePart{
		{Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "first"}},
		{Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "caption"}},
		{Content: &event.MessageEventContent{MsgType: event.MsgImage, Body: "img.png"}},
	}
	props := json.RawMessage(`{"links":[
		{"originalUrl":" https://example.com/a ","title":"A","description":"About A","previewimage":"https://example.com/a.png","siteName":"Example"},
		{"originalUrl":"https://example.com/b"},
		{"title":"no url"}
	]}`)
	applyLinkPreviews(parts, props)

	want := []map[string]any{
		{"matched_url": "https://example.com/a", "og:title": "A", "og:description": "About A", "og:image": "https://example.com/a.png", "og:site_name": "Example"},
		{"matched_url": "https://example.com/b"},
	}
	if got := parts[1].Extra["com.beeper.linkpreviews"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("previews on caption = %#v, want %#v", got, want)
	}
	if parts[0].Extra != nil || parts[2].Extra != nil {
		t.Fatalf("previews must go on the last text part only: %#v / %#v", parts[0].Extra, parts[2].Extra)
	}
}

func TestApplyLinkPreviewsNoneOrNoTextPart(t *testing.T) {
	image := &bridgev2.ConvertedMessagePart{Content: &event.MessageEventContent{MsgType: event.MsgImage}}
	applyLinkPreviews([]*bridgev2.ConvertedMessagePart{image}, json.RawMessage(`{"links":[{"originalUrl":"https://x"}]}`))
	if image.Extra != nil {
		t.Fatalf("no text part: Extra must stay nil, got %#v", image.Extra)
	}
	text := &bridgev2.ConvertedMessagePart{Content: &event.MessageEventContent{MsgType: event.MsgText}}
	applyLinkPreviews([]*bridgev2.ConvertedMessagePart{text}, json.RawMessage(`{}`))
	if text.Extra != nil {
		t.Fatalf("no previews: Extra must stay nil, got %#v", text.Extra)
	}
}
