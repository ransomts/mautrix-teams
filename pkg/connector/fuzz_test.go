package connector

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Fuzz tests for functions that parse untrusted input
// ---------------------------------------------------------------------------

// FuzzStripHTMLFallback fuzzes the HTML stripping function used for plaintext
// fallback generation. Malformed HTML should never cause a panic.
func FuzzStripHTMLFallback(f *testing.F) {
	f.Add("<p>hello</p>")
	f.Add("<br>line<br/>break")
	f.Add("")
	f.Add("<script>alert('xss')</script>")
	f.Add("<<>><<<>>>")
	f.Add("<p><b>nested</b></p>")
	f.Add(string(make([]byte, 10000)))

	f.Fuzz(func(t *testing.T, input string) {
		// Should never panic
		result := stripHTMLFallback(input)
		_ = result
	})
}

// FuzzPlainTextToHTML fuzzes the plaintext-to-HTML converter. Should always
// produce safe output (no unescaped user content).
func FuzzPlainTextToHTML(f *testing.F) {
	f.Add("hello world")
	f.Add("<script>alert(1)</script>")
	f.Add("line1\nline2")
	f.Add("")
	f.Add("a & b < c > d")
	f.Add("\"quotes\" and 'apostrophes'")

	f.Fuzz(func(t *testing.T, input string) {
		result := plainTextToHTML(input)
		_ = result
	})
}

// FuzzDetectMIMEType fuzzes MIME type detection with various filename and
// content type combinations.
func FuzzDetectMIMEType(f *testing.F) {
	f.Add("file.png", "image/png", []byte{})
	f.Add("", "", []byte{})
	f.Add("test.unknown", "application/octet-stream", []byte{0x89, 0x50, 0x4E, 0x47})
	f.Add("../../etc/passwd", "text/plain", []byte{})

	f.Fuzz(func(t *testing.T, filename, contentType string, data []byte) {
		result := detectMIMEType(filename, contentType, data)
		if result == "" {
			t.Error("detectMIMEType should never return empty string")
		}
	})
}

// FuzzMapEmojiToEmotionKey fuzzes the emoji-to-emotion-key mapper.
func FuzzMapEmojiToEmotionKey(f *testing.F) {
	f.Add("👍")
	f.Add("❤️")
	f.Add("")
	f.Add("hello")
	f.Add("🦊")
	f.Add("a")
	f.Add(string([]byte{0xFF, 0xFE}))

	f.Fuzz(func(t *testing.T, input string) {
		key, ok := MapEmojiToEmotionKey(input)
		if ok && key == "" {
			t.Error("ok=true but key is empty")
		}
	})
}

// FuzzMapEmotionKeyToEmoji fuzzes the emotion-key-to-emoji mapper.
func FuzzMapEmotionKeyToEmoji(f *testing.F) {
	f.Add("like")
	f.Add("heart")
	f.Add("")
	f.Add("custom_emoji")
	f.Add("🦊")

	f.Fuzz(func(t *testing.T, input string) {
		emoji, ok := MapEmotionKeyToEmoji(input)
		if ok && emoji == "" {
			t.Error("ok=true but emoji is empty")
		}
	})
}

// FuzzExtractMeetingJoinURL fuzzes URL extraction from message bodies.
func FuzzExtractMeetingJoinURL(f *testing.F) {
	f.Add("Join: https://teams.microsoft.com/l/meetup-join/abc123")
	f.Add("")
	f.Add("no url here")
	f.Add(`<a href="https://teams.microsoft.com/l/meetup-join/x">click</a>`)

	f.Fuzz(func(t *testing.T, input string) {
		result := extractMeetingJoinURL(input)
		_ = result
	})
}

// FuzzNormalizeTeamsReactionMessageID fuzzes message ID normalization.
func FuzzNormalizeTeamsReactionMessageID(f *testing.F) {
	f.Add("msg/12345")
	f.Add("12345")
	f.Add("")
	f.Add("msg/")
	f.Add("  msg/abc  ")

	f.Fuzz(func(t *testing.T, input string) {
		result := NormalizeTeamsReactionMessageID(input)
		_ = result
	})
}

// FuzzRewriteAMSURL fuzzes AMS URL rewriting.
func FuzzRewriteAMSURL(f *testing.F) {
	f.Add("https://us-api.asm.skype.com/v1/objects/abc", "https://region.test")
	f.Add("", "")
	f.Add("://bad", "https://region.test")
	f.Add("https://valid.test/path", "://bad-region")

	f.Fuzz(func(t *testing.T, imageURL, region string) {
		result := rewriteAMSURL(imageURL, region)
		_ = result
	})
}
