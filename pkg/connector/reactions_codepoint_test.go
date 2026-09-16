package connector

import (
	"strings"
	"testing"

	"go.mau.fi/util/variationselector"
)

func TestMapEmotionKeyDecodesCodepointKeys(t *testing.T) {
	cases := map[string]string{
		"2757_heavyexclamationmarksymbol":  "❗",
		"1f440_eyes":                       "👀",
		"2714_heavycheckmark":              "✔",
		"1f4af_hundredpointssymbol":        "💯",
		"1f468_200d_1f4bb_mantechnologist": "👨\u200d💻",
	}
	for key, want := range cases {
		got, ok := MapEmotionKeyToEmoji(key)
		if !ok || variationselector.Remove(got) != variationselector.Remove(want) {
			t.Errorf("%s: got %q ok=%v, want %q", key, got, ok, want)
		}
	}
}

func TestMapEmotionKeyAliasesAndSkinTones(t *testing.T) {
	if got, _ := MapEmotionKeyToEmoji("yes"); variationselector.Remove(got) != "👍" {
		t.Fatalf("yes -> %q", got)
	}
	if got, _ := MapEmotionKeyToEmoji("yes-tone1"); !strings.HasSuffix(got, "\U0001F3FB") || !strings.HasPrefix(variationselector.Remove(got), "👍") {
		t.Fatalf("yes-tone1 -> %q", got)
	}
	if got, _ := MapEmotionKeyToEmoji("like-tone3"); !strings.HasSuffix(got, "\U0001F3FD") {
		t.Fatalf("like-tone3 -> %q", got)
	}
	if got, _ := MapEmotionKeyToEmoji("pinchedfingers"); variationselector.Remove(got) != "🤌" {
		t.Fatalf("pinchedfingers -> %q", got)
	}
}

func TestMapEmotionKeyKeepsShortcodeForUnknownAndHexWords(t *testing.T) {
	if got, _ := MapEmotionKeyToEmoji("customorgemoji"); got != ":customorgemoji:" {
		t.Fatalf("unknown key -> %q", got)
	}
	if got, _ := MapEmotionKeyToEmoji("beef"); got != ":beef:" {
		t.Fatalf("hex-looking word must not decode: %q", got)
	}
	// Existing mapping unaffected.
	if got, _ := MapEmotionKeyToEmoji("like"); variationselector.Remove(got) != "👍" {
		t.Fatalf("like -> %q", got)
	}
	if key, ok := MapEmojiToEmotionKey("👍"); !ok || key != "like" {
		t.Fatalf("reverse mapping changed: %q %v", key, ok)
	}
}
