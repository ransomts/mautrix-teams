package connector

import (
	"testing"

	"go.mau.fi/util/variationselector"
)

// ---------------------------------------------------------------------------
// Table-driven emoji mapping tests
// ---------------------------------------------------------------------------

func TestMapEmojiToEmotionKey_Table(t *testing.T) {
	tests := []struct {
		name    string
		emoji   string
		wantKey string
		wantOK  bool
	}{
		{"thumbs up", "👍", "like", true},
		{"heart", "❤️", "heart", true},
		{"fire", "🔥", "fire", true},
		{"smile/nod", "🙂", "nod", true},
		{"empty string", "", "", false},
		{"whitespace only", "  ", "", false},
		{"unknown unicode emoji passthrough", "🦊", "🦊", true},
		{"ASCII text rejected", "hello", "", false},
		{"single ASCII char", "a", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := MapEmojiToEmotionKey(tt.emoji)
			if ok != tt.wantOK {
				t.Errorf("MapEmojiToEmotionKey(%q) ok = %v, want %v", tt.emoji, ok, tt.wantOK)
			}
			if key != tt.wantKey {
				t.Errorf("MapEmojiToEmotionKey(%q) = %q, want %q", tt.emoji, key, tt.wantKey)
			}
		})
	}
}

func TestMapEmotionKeyToEmoji_Table(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantEmoji string
		wantOK    bool
	}{
		// "like" maps to the first entry found in the inverse map;
		// actual value depends on map iteration order, just check non-empty.
		{"heart", "heart", "❤️", true},
		{"fire", "fire", "🔥", true},
		{"inbound-only alias", "expressionless", "😑", true},
		{"empty", "", "", false},
		{"custom org emoji", "custom_sparkle", ":custom_sparkle:", true},
		{"unicode passthrough", "🦊", "🦊", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emoji, ok := MapEmotionKeyToEmoji(tt.key)
			if ok != tt.wantOK {
				t.Errorf("MapEmotionKeyToEmoji(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			}
			if emoji != tt.wantEmoji {
				t.Errorf("MapEmotionKeyToEmoji(%q) = %q, want %q", tt.key, emoji, tt.wantEmoji)
			}
		})
	}

	// "like" maps to either 👍 or 👍🏻 depending on map iteration order.
	t.Run("like returns non-empty", func(t *testing.T) {
		emoji, ok := MapEmotionKeyToEmoji("like")
		if !ok || emoji == "" {
			t.Error("expected non-empty emoji for 'like'")
		}
	})
}

func TestIsUnicodeEmoji_Table(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{"single emoji", "🦊", true},
		{"emoji with skin tone", "👍🏻", true},
		{"empty", "", false},
		{"ASCII", "hello", false},
		{"single ASCII char", "a", false},
		{"very long string", "🦊🦊🦊🦊🦊🦊🦊🦊🦊🦊🦊", false}, // >20 bytes
		{"whitespace only", "  ", false},
		{"flag emoji", "🇺🇸", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUnicodeEmoji(tt.s)
			if got != tt.want {
				t.Errorf("IsUnicodeEmoji(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Normalize functions
// ---------------------------------------------------------------------------

func TestNormalizeTeamsReactionMessageID_Table(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"msg/12345", "12345"},
		{"12345", "12345"},
		{"", ""},
		{"  msg/abc  ", "abc"},
		{"msg/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeTeamsReactionMessageID(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeTeamsReactionMessageID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Roundtrip: emoji -> key -> emoji
// ---------------------------------------------------------------------------

func TestEmojiRoundtrip(t *testing.T) {
	// Test known emojis roundtrip correctly
	knownEmojis := []string{"👍", "❤️", "🔥", "😂", "😍", "🎉"}
	for _, emoji := range knownEmojis {
		key, ok := MapEmojiToEmotionKey(emoji)
		if !ok {
			t.Errorf("MapEmojiToEmotionKey(%q) failed", emoji)
			continue
		}
		back, ok := MapEmotionKeyToEmoji(key)
		if !ok {
			t.Errorf("MapEmotionKeyToEmoji(%q) failed", key)
			continue
		}
		// The roundtrip may not be exact due to variation selectors,
		// but the key should map back to a valid emoji.
		if back == "" {
			t.Errorf("roundtrip for %q produced empty emoji", emoji)
		}
	}
}

func TestEmojiTablesLoaded(t *testing.T) {
	if len(teamsKeyToEmoji) < 100 || len(unicodeNameToEmoji) < 1000 {
		t.Fatalf("emoji tables too small: %d Teams keys, %d Unicode names", len(teamsKeyToEmoji), len(unicodeNameToEmoji))
	}
}

// Keys once listed by hand in emotionKeyAliases, now covered by the
// generated tables, decode as before.
func TestMapEmotionKeyToEmoji_GeneratedTables(t *testing.T) {
	for key, want := range map[string]string{
		"smileeyes":           "😊",
		"clappinghands":       "👏",
		"snake":               "🐍",
		"smilingfacewithtear": "🥲",
		"pinchedfingers":      "🤌",
		"eyes":                "👀",
		"heavycheckmark":      "✔️",
		"hundredpointssymbol": "💯",
		"hearteyes":           "😍",
		"ghost":               "👻",
		"expressionless":      "😑",
		// Teams' own table wins over the hand-made outbound one.
		"speechless": "😐",
		// Legacy Skype names stay in emotionKeyAliases.
		"laughcry": "😂",
		"fistbump": "👊",
	} {
		if got, ok := MapEmotionKeyToEmoji(key); !ok || got != variationselector.FullyQualify(want) {
			t.Errorf("MapEmotionKeyToEmoji(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
}

func TestMapEmojiToEmotionKey_SkinToneKeepsKey(t *testing.T) {
	if got, _ := MapEmojiToEmotionKey("👍🏻"); got != "like" {
		t.Errorf("MapEmojiToEmotionKey(👍🏻) = %q, want like", got)
	}
	if got, _ := MapEmojiToEmotionKey("😶"); got == "speechless" {
		t.Errorf("😶 went out as speechless, which Teams draws as 😐")
	}
}

// An emoji whose hand-mapped key Teams draws differently goes out under
// Teams' own key for it.
func TestMapEmojiToEmotionKey_AgreesWithTeams(t *testing.T) {
	for emoji, key := range emojiToEmotionKey {
		got, ok := MapEmojiToEmotionKey(emoji)
		if !ok {
			t.Errorf("MapEmojiToEmotionKey(%q) failed", emoji)
			continue
		}
		if back, _ := MapEmotionKeyToEmoji(got); teamsKeyToEmoji[got] != "" && back != stripSkinTones(emoji) {
			t.Errorf("%q (hand key %q) goes out as %q, which Teams draws as %q", emoji, key, got, back)
		}
	}
}
