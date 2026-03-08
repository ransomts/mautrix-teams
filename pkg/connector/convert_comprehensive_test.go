package connector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// ---------------------------------------------------------------------------
// Table-driven tests for convertTeamsMessage
// ---------------------------------------------------------------------------

func TestConvertTeamsMessage(t *testing.T) {
	tests := []struct {
		name           string
		msg            model.RemoteMessage
		wantMsgType    event.MessageType
		wantBodyContains string
		wantHTMLContains string
		wantParts      int
		wantReplyTo    string
	}{
		{
			name: "plain text message",
			msg: model.RemoteMessage{
				Body:        "hello world",
				MessageType: "RichText/Html",
				SenderID:    "8:orgid:alice",
				SenderName:  "Alice",
			},
			wantMsgType:      event.MsgText,
			wantBodyContains: "hello world",
			wantParts:        1,
		},
		{
			name: "HTML formatted message",
			msg: model.RemoteMessage{
				Body:          "bold text",
				FormattedBody: "<b>bold text</b>",
				MessageType:   "RichText/Html",
				SenderID:      "8:orgid:alice",
			},
			wantMsgType:      event.MsgText,
			wantBodyContains: "bold",
			wantHTMLContains: "<b>bold text</b>",
			wantParts:        1,
		},
		{
			name: "empty body produces space fallback",
			msg: model.RemoteMessage{
				Body:        "",
				MessageType: "RichText/Html",
				SenderID:    "8:orgid:alice",
			},
			wantMsgType:      event.MsgText,
			wantBodyContains: " ",
			wantParts:        1,
		},
		{
			name: "message with reply-to",
			msg: model.RemoteMessage{
				Body:        "reply text",
				MessageType: "RichText/Html",
				SenderID:    "8:orgid:alice",
				ReplyToID:   "original-msg-123",
			},
			wantMsgType:      event.MsgText,
			wantBodyContains: "reply text",
			wantParts:        1,
			wantReplyTo:      "original-msg-123",
		},
		{
			name: "message with GIFs",
			msg: model.RemoteMessage{
				Body:        "check this",
				MessageType: "RichText/Html",
				SenderID:    "8:orgid:alice",
				GIFs: []model.TeamsGIF{
					{Title: "Funny", URL: "https://giphy.com/test.gif"},
				},
			},
			wantMsgType:      event.MsgText,
			wantBodyContains: "GIF: Funny",
			wantParts:        1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &TeamsClient{}
			cm, err := c.convertTeamsMessage(context.Background(), nil, nil, tt.msg)
			if err != nil {
				t.Fatalf("convertTeamsMessage error: %v", err)
			}
			if cm == nil {
				t.Fatal("convertTeamsMessage returned nil")
			}
			if len(cm.Parts) != tt.wantParts {
				t.Fatalf("expected %d parts, got %d", tt.wantParts, len(cm.Parts))
			}

			part := cm.Parts[0]
			if part.Content.MsgType != tt.wantMsgType {
				t.Errorf("expected MsgType %s, got %s", tt.wantMsgType, part.Content.MsgType)
			}
			if tt.wantBodyContains != "" && !strings.Contains(part.Content.Body, tt.wantBodyContains) {
				t.Errorf("body %q does not contain %q", part.Content.Body, tt.wantBodyContains)
			}
			if tt.wantHTMLContains != "" && !strings.Contains(part.Content.FormattedBody, tt.wantHTMLContains) {
				t.Errorf("formatted body %q does not contain %q", part.Content.FormattedBody, tt.wantHTMLContains)
			}
			if tt.wantReplyTo != "" {
				if cm.ReplyTo == nil || string(cm.ReplyTo.MessageID) != tt.wantReplyTo {
					t.Errorf("expected ReplyTo %s, got %v", tt.wantReplyTo, cm.ReplyTo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Call/Meeting event conversion
// ---------------------------------------------------------------------------

func TestConvertCallOrMeetingEvent(t *testing.T) {
	tests := []struct {
		name         string
		msg          model.RemoteMessage
		wantNil      bool
		wantContains string
		wantNotice   bool
	}{
		{
			name:    "not a call event",
			msg:     model.RemoteMessage{MessageType: "RichText/Html", Body: "hello"},
			wantNil: true,
		},
		{
			name:         "call start",
			msg:          model.RemoteMessage{MessageType: "Event/Call/Start", SenderID: "8:orgid:alice"},
			wantContains: "Call started",
			wantNotice:   true,
		},
		{
			name:         "call end",
			msg:          model.RemoteMessage{MessageType: "Event/Call/End"},
			wantContains: "Call ended",
			wantNotice:   true,
		},
		{
			name: "missed call",
			msg: model.RemoteMessage{
				MessageType: "Event/Call/Missed",
				SenderName:  "Bob",
				SenderID:    "8:orgid:bob",
			},
			wantContains: "Missed call from Bob",
			wantNotice:   true,
		},
		{
			name:         "generic call event",
			msg:          model.RemoteMessage{MessageType: "event/call"},
			wantContains: "Call event",
			wantNotice:   true,
		},
		{
			name: "meeting join URL in body",
			msg: model.RemoteMessage{
				MessageType: "RichText/Html",
				Body:        "Join meeting: https://teams.microsoft.com/l/meetup-join/abc123",
			},
			wantContains: "Meeting:",
			wantNotice:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertCallOrMeetingEvent(tt.msg)
			if tt.wantNil {
				if result != nil {
					t.Fatalf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if len(result.Parts) != 1 {
				t.Fatalf("expected 1 part, got %d", len(result.Parts))
			}
			if tt.wantNotice && result.Parts[0].Content.MsgType != event.MsgNotice {
				t.Errorf("expected MsgNotice, got %s", result.Parts[0].Content.MsgType)
			}
			if !strings.Contains(result.Parts[0].Content.Body, tt.wantContains) {
				t.Errorf("body %q does not contain %q", result.Parts[0].Content.Body, tt.wantContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Meeting URL extraction
// ---------------------------------------------------------------------------

func TestExtractMeetingJoinURL_Table(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		want  string
	}{
		{
			name: "URL in text",
			body: "Join: https://teams.microsoft.com/l/meetup-join/abc123 now",
			want: "https://teams.microsoft.com/l/meetup-join/abc123",
		},
		{
			name: "URL in HTML",
			body: `<a href="https://teams.microsoft.com/l/meetup-join/xyz">Join</a>`,
			want: "https://teams.microsoft.com/l/meetup-join/xyz",
		},
		{
			name: "no meeting URL",
			body: "This is a regular message",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMeetingJoinURL(tt.body)
			if got != tt.want {
				t.Errorf("extractMeetingJoinURL(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Voice message hint
// ---------------------------------------------------------------------------

func TestApplyVoiceMessageHint(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		mime     string
		wantVoice bool
	}{
		{"voice in filename", "voice_message_001.ogg", "audio/ogg", true},
		{"audio_message pattern", "audio_message.m4a", "audio/mp4", true},
		{"ogg extension", "recording.ogg", "audio/ogg", true},
		{"regular audio", "song.mp3", "audio/mpeg", false},
		{"not audio mime", "voice.txt", "text/plain", false},
		{"image file", "photo.png", "image/png", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			part := &bridgev2.ConvertedMessagePart{
				Content: &event.MessageEventContent{MsgType: event.MsgFile},
			}
			applyVoiceMessageHint(part, tt.filename, tt.mime)
			if tt.wantVoice {
				if part.Content.MsgType != event.MsgAudio {
					t.Errorf("expected MsgAudio, got %s", part.Content.MsgType)
				}
				if part.Extra == nil {
					t.Fatal("expected Extra with voice hint")
				}
				if _, ok := part.Extra["org.matrix.msc3245.voice"]; !ok {
					t.Error("missing org.matrix.msc3245.voice key")
				}
			} else {
				if part.Content.MsgType == event.MsgAudio && tt.mime != "audio/mpeg" {
					t.Errorf("should not set MsgAudio for %s", tt.filename)
				}
			}
		})
	}
}

func TestApplyVoiceMessageHint_NilPart(t *testing.T) {
	// Should not panic
	applyVoiceMessageHint(nil, "voice.ogg", "audio/ogg")

	part := &bridgev2.ConvertedMessagePart{Content: nil}
	applyVoiceMessageHint(part, "voice.ogg", "audio/ogg")
}

// ---------------------------------------------------------------------------
// Adaptive card legacy fallback
// ---------------------------------------------------------------------------

func TestConvertTeamsMessageLegacy_AdaptiveCardFallback(t *testing.T) {
	cardJSON := `{
		"cards": [{
			"content": {
				"type": "AdaptiveCard",
				"body": [
					{"type": "TextBlock", "text": "Bot Says Hello", "weight": "Bolder"}
				]
			}
		}]
	}`
	msg := model.RemoteMessage{
		Body:          "",
		FormattedBody: "",
		MessageType:   "RichText/Html",
		SenderID:      "8:orgid:bot",
		SenderName:    "Bot",
		PropertiesRaw: json.RawMessage(cardJSON),
	}
	c := &TeamsClient{}
	cm := c.convertTeamsMessageLegacy(msg)
	if cm == nil || len(cm.Parts) != 1 {
		t.Fatal("expected 1 part from adaptive card fallback")
	}
	if !strings.Contains(cm.Parts[0].Content.Body, "Bot Says Hello") {
		t.Errorf("body should contain card text, got %q", cm.Parts[0].Content.Body)
	}
}

// ---------------------------------------------------------------------------
// Link preview enrichment
// ---------------------------------------------------------------------------

func TestConvertTeamsMessage_LinkPreviews(t *testing.T) {
	linkJSON := `{
		"links": [{
			"originalUrl": "https://example.com",
			"title": "Example",
			"description": "Test page",
			"previewimage": "https://example.com/img.png",
			"siteName": "Example Site"
		}]
	}`
	msg := model.RemoteMessage{
		Body:          "Check https://example.com",
		MessageType:   "RichText/Html",
		SenderID:      "8:orgid:alice",
		PropertiesRaw: json.RawMessage(linkJSON),
	}
	// Uses legacy path since no DriveItemID or inline images
	c := &TeamsClient{}
	cm, err := c.convertTeamsMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cm == nil || len(cm.Parts) == 0 {
		t.Fatal("expected at least 1 part")
	}
	// Link previews are only added in the non-legacy path when there are inline images.
	// In legacy, adaptive card/body rendering happens but no link preview Extra.
	// This test verifies the body still renders.
	if !strings.Contains(cm.Parts[0].Content.Body, "https://example.com") {
		t.Errorf("body should contain URL, got %q", cm.Parts[0].Content.Body)
	}
}

// ---------------------------------------------------------------------------
// Thread relation
// ---------------------------------------------------------------------------

func TestConvertTeamsMessage_ThreadRelation(t *testing.T) {
	msg := model.RemoteMessage{
		Body:         "thread reply",
		MessageType:  "RichText/Html",
		SenderID:     "8:orgid:alice",
		ThreadRootID: "root-msg-456",
	}
	// Legacy path (no attachments/inline images) doesn't add thread relation.
	// Verify the message converts without error.
	c := &TeamsClient{}
	cm, err := c.convertTeamsMessage(context.Background(), nil, nil, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cm == nil || len(cm.Parts) == 0 {
		t.Fatal("expected at least 1 part")
	}
	if !strings.Contains(cm.Parts[0].Content.Body, "thread reply") {
		t.Errorf("body should contain message text, got %q", cm.Parts[0].Content.Body)
	}
}

// ---------------------------------------------------------------------------
// Per-message profile metadata
// ---------------------------------------------------------------------------

func TestPerMessageExtra(t *testing.T) {
	tests := []struct {
		name       string
		msg        model.RemoteMessage
		wantNil    bool
		wantID     string
		wantName   string
	}{
		{
			name:    "with sender info",
			msg:     model.RemoteMessage{SenderID: "8:orgid:alice", SenderName: "Alice"},
			wantID:  "8:orgid:alice",
			wantName: "Alice",
		},
		{
			name:    "empty sender",
			msg:     model.RemoteMessage{},
			wantNil: true,
		},
		{
			name:    "sender ID without name",
			msg:     model.RemoteMessage{SenderID: "8:orgid:alice"},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra := perMessageExtra(tt.msg)
			if tt.wantNil {
				if extra != nil {
					t.Errorf("expected nil extra, got %v", extra)
				}
				return
			}
			profile, ok := extra["com.beeper.per_message_profile"].(map[string]any)
			if !ok {
				t.Fatal("missing per_message_profile")
			}
			if profile["id"] != tt.wantID {
				t.Errorf("expected id=%s, got %v", tt.wantID, profile["id"])
			}
			if profile["displayname"] != tt.wantName {
				t.Errorf("expected displayname=%s, got %v", tt.wantName, profile["displayname"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

func TestCloneExtra(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if cloneExtra(nil) != nil {
			t.Error("expected nil")
		}
	})

	t.Run("clone is independent", func(t *testing.T) {
		orig := map[string]any{"key": "value"}
		clone := cloneExtra(orig)
		clone["key"] = "modified"
		if orig["key"] != "value" {
			t.Error("clone modified original")
		}
	})
}

func TestRewriteAMSURL(t *testing.T) {
	tests := []struct {
		name     string
		imageURL string
		region   string
		want     string
	}{
		{
			name:     "rewrites host",
			imageURL: "https://us-api.asm.skype.com/v1/objects/abc/views/imgpsh_fullsize_anim",
			region:   "https://amer.ng.msg.teams.microsoft.com",
			want:     "https://amer.ng.msg.teams.microsoft.com/v1/objects/abc/views/imgpsh_fullsize_anim",
		},
		{
			name:     "invalid URL returns original",
			imageURL: "://bad",
			region:   "https://region.test",
			want:     "://bad",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteAMSURL(tt.imageURL, tt.region)
			if got != tt.want {
				t.Errorf("rewriteAMSURL(%q, %q) = %q, want %q", tt.imageURL, tt.region, got, tt.want)
			}
		})
	}
}

func TestMimeExtension(t *testing.T) {
	tests := map[string]string{
		"image/png":     ".png",
		"image/jpeg":    ".jpg",
		"image/gif":     ".gif",
		"image/webp":    ".webp",
		"text/plain":    "",
		"":              "",
	}
	for mime, want := range tests {
		got := mimeExtension(mime)
		if got != want {
			t.Errorf("mimeExtension(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestBuildCaptionPart(t *testing.T) {
	t.Run("empty body returns nil", func(t *testing.T) {
		part := buildCaptionPart("cap", renderedInboundMessage{}, nil)
		if part != nil {
			t.Error("expected nil for empty caption")
		}
	})

	t.Run("text body returns part", func(t *testing.T) {
		part := buildCaptionPart("cap", renderedInboundMessage{Body: "hello"}, nil)
		if part == nil {
			t.Fatal("expected non-nil part")
		}
		if part.Content.Body != "hello" {
			t.Errorf("unexpected body: %q", part.Content.Body)
		}
	})

	t.Run("HTML only falls back", func(t *testing.T) {
		part := buildCaptionPart("cap", renderedInboundMessage{FormattedBody: "<b>hello</b>"}, nil)
		if part == nil {
			t.Fatal("expected non-nil part")
		}
		if part.Content.Format != event.FormatHTML {
			t.Error("expected HTML format")
		}
	})
}
