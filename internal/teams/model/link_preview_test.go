package model

import (
	"encoding/json"
	"testing"
)

func TestExtractLinkPreviews(t *testing.T) {
	props := json.RawMessage(`{
		"links": [
			{
				"originalUrl": "https://example.com/page",
				"title": "Example Page",
				"description": "A test page",
				"previewimage": "https://example.com/img.png",
				"siteName": "Example"
			}
		]
	}`)

	previews := ExtractLinkPreviews(props)
	if len(previews) != 1 {
		t.Fatalf("expected 1 preview, got %d", len(previews))
	}
	if previews[0].URL != "https://example.com/page" {
		t.Errorf("unexpected url: %s", previews[0].URL)
	}
	if previews[0].Title != "Example Page" {
		t.Errorf("unexpected title: %s", previews[0].Title)
	}
	if previews[0].ImageURL != "https://example.com/img.png" {
		t.Errorf("unexpected image url: %s", previews[0].ImageURL)
	}
}

func TestExtractLinkPreviews_Empty(t *testing.T) {
	previews := ExtractLinkPreviews(json.RawMessage(`{}`))
	if len(previews) != 0 {
		t.Errorf("expected 0 previews, got %d", len(previews))
	}
}

func TestExtractLinkPreviews_Nil(t *testing.T) {
	previews := ExtractLinkPreviews(nil)
	if previews != nil {
		t.Error("expected nil for nil properties")
	}
}

func TestExtractLinkPreviews_SkipsEmptyURL(t *testing.T) {
	props := json.RawMessage(`{
		"links": [
			{"originalUrl": "", "title": "No URL"},
			{"originalUrl": "https://valid.com", "title": "Valid"}
		]
	}`)
	previews := ExtractLinkPreviews(props)
	if len(previews) != 1 {
		t.Fatalf("expected 1 preview (empty URL skipped), got %d", len(previews))
	}
	if previews[0].URL != "https://valid.com" {
		t.Errorf("unexpected url: %s", previews[0].URL)
	}
}
