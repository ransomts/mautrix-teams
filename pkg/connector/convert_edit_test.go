package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func TestConvertTeamsEditBasic(t *testing.T) {
	c := &TeamsClient{}
	existing := []*database.Message{
		{ID: networkid.MessageID("msg1")},
	}
	msg := model.RemoteMessage{
		Body:          "updated text",
		FormattedBody: "",
	}

	result, err := c.convertTeamsEdit(context.Background(), nil, nil, existing, msg)
	if err != nil {
		t.Fatalf("convertTeamsEdit failed: %v", err)
	}
	if result == nil || len(result.ModifiedParts) != 1 {
		t.Fatalf("expected 1 modified part, got %v", result)
	}
	if result.ModifiedParts[0].Content.Body != "updated text" {
		t.Fatalf("unexpected body: %q", result.ModifiedParts[0].Content.Body)
	}
	if result.ModifiedParts[0].Content.Format != "" {
		t.Fatalf("expected no format for plain text edit, got %q", result.ModifiedParts[0].Content.Format)
	}
}

func TestConvertTeamsEditWithHTML(t *testing.T) {
	c := &TeamsClient{}
	existing := []*database.Message{
		{ID: networkid.MessageID("msg1")},
	}
	msg := model.RemoteMessage{
		Body:          "bold text",
		FormattedBody: "<b>bold text</b>",
	}

	result, err := c.convertTeamsEdit(context.Background(), nil, nil, existing, msg)
	if err != nil {
		t.Fatalf("convertTeamsEdit failed: %v", err)
	}
	if result.ModifiedParts[0].Content.Format != event.FormatHTML {
		t.Fatalf("expected HTML format, got %q", result.ModifiedParts[0].Content.Format)
	}
	if result.ModifiedParts[0].Content.FormattedBody != "<b>bold text</b>" {
		t.Fatalf("unexpected formatted body: %q", result.ModifiedParts[0].Content.FormattedBody)
	}
}

func TestConvertTeamsEditEmptyBody(t *testing.T) {
	c := &TeamsClient{}
	existing := []*database.Message{
		{ID: networkid.MessageID("msg1")},
	}
	msg := model.RemoteMessage{
		Body: "  ",
	}

	result, err := c.convertTeamsEdit(context.Background(), nil, nil, existing, msg)
	if err != nil {
		t.Fatalf("convertTeamsEdit failed: %v", err)
	}
	if result.ModifiedParts[0].Content.Body != " " {
		t.Fatalf("expected single space fallback, got %q", result.ModifiedParts[0].Content.Body)
	}
}

func TestConvertTeamsEditNoExistingParts(t *testing.T) {
	c := &TeamsClient{}
	msg := model.RemoteMessage{Body: "updated"}

	_, err := c.convertTeamsEdit(context.Background(), nil, nil, nil, msg)
	if err == nil {
		t.Fatalf("expected error for nil existing parts")
	}
}

func TestStripHTMLFallback(t *testing.T) {
	cases := map[string]string{
		"<p>hello</p>":              "hello",
		"line1<br>line2":            "line1\nline2",
		"line1<br/>line2":           "line1\nline2",
		"line1<br />line2":          "line1\nline2",
		"<p>a</p><p>b<br>c</p>":    "ab\nc",
	}
	for input, expected := range cases {
		got := stripHTMLFallback(input)
		if got != expected {
			t.Fatalf("stripHTMLFallback(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestPlainTextToHTML(t *testing.T) {
	cases := map[string]string{
		"hello":                "hello",
		"line1\nline2":         "line1<br>line2",
		"<script>alert</script>": "&lt;script&gt;alert&lt;/script&gt;",
		"a & b":                "a &amp; b",
	}
	for input, expected := range cases {
		got := plainTextToHTML(input)
		if got != expected {
			t.Fatalf("plainTextToHTML(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestDetectMIMEType(t *testing.T) {
	// Header content type takes priority.
	got := detectMIMEType("file.txt", "image/png", nil)
	if got != "image/png" {
		t.Fatalf("expected image/png from header, got %q", got)
	}

	// Fallback to extension when header is generic.
	got = detectMIMEType("photo.jpg", "application/octet-stream", nil)
	if got != "image/jpeg" {
		t.Fatalf("expected image/jpeg from extension, got %q", got)
	}

	// Fallback to octet-stream when nothing else works.
	got = detectMIMEType("", "", nil)
	if got != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream fallback, got %q", got)
	}
}

func TestMatrixMsgTypeForMIME(t *testing.T) {
	cases := map[string]event.MessageType{
		"image/png":            event.MsgImage,
		"image/jpeg":           event.MsgImage,
		"video/mp4":            event.MsgVideo,
		"audio/mpeg":           event.MsgAudio,
		"application/pdf":      event.MsgFile,
		"application/zip":      event.MsgFile,
	}
	for mime, expected := range cases {
		got := matrixMsgTypeForMIME(mime)
		if got != expected {
			t.Fatalf("matrixMsgTypeForMIME(%q) = %q, want %q", mime, got, expected)
		}
	}
}
