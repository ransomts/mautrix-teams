package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractAdaptiveCards(t *testing.T) {
	props := json.RawMessage(`{
		"cards": [{
			"content": {
				"type": "AdaptiveCard",
				"body": [
					{"type": "TextBlock", "text": "Hello World", "weight": "Bolder"},
					{"type": "TextBlock", "text": "This is a card"}
				],
				"actions": [
					{"type": "Action.OpenUrl", "title": "Open", "url": "https://example.com"}
				]
			}
		}]
	}`)

	cards := ExtractAdaptiveCards(props)
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if len(cards[0].Body) != 2 {
		t.Fatalf("expected 2 body elements, got %d", len(cards[0].Body))
	}
	if len(cards[0].Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(cards[0].Actions))
	}
}

func TestAdaptiveCard_RenderPlainText(t *testing.T) {
	card := AdaptiveCard{
		Body: []CardElement{
			{Type: "TextBlock", Text: "Title"},
			{Type: "TextBlock", Text: "Description"},
		},
		Actions: []CardAction{
			{Type: "Action.OpenUrl", Title: "Click", URL: "https://example.com"},
		},
	}
	text := card.RenderPlainText()
	if !strings.Contains(text, "Title") {
		t.Error("expected plain text to contain Title")
	}
	if !strings.Contains(text, "[Click](https://example.com)") {
		t.Errorf("expected action link, got: %s", text)
	}
}

func TestAdaptiveCard_RenderHTML(t *testing.T) {
	card := AdaptiveCard{
		Body: []CardElement{
			{Type: "TextBlock", Text: "Bold Title", Weight: "Bolder"},
		},
	}
	html := card.RenderHTML()
	if !strings.Contains(html, "<strong>Bold Title</strong>") {
		t.Errorf("expected bold title, got: %s", html)
	}
	if !strings.Contains(html, "<blockquote>") {
		t.Error("expected blockquote wrapper")
	}
}

func TestExtractAdaptiveCards_NoCards(t *testing.T) {
	cards := ExtractAdaptiveCards(json.RawMessage(`{}`))
	if len(cards) != 0 {
		t.Errorf("expected 0 cards, got %d", len(cards))
	}
}

func TestExtractAdaptiveCards_NilProperties(t *testing.T) {
	cards := ExtractAdaptiveCards(nil)
	if cards != nil {
		t.Error("expected nil for nil properties")
	}
}
