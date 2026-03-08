package connector

import (
	"strings"

	"go.mau.fi/util/variationselector"
)

var emojiToEmotionKey = map[string]string{
	variationselector.FullyQualify("👍🏻"): "like",
	variationselector.FullyQualify("👌🏻"): "ok",
	variationselector.FullyQualify("🔥"):  "fire",
	variationselector.FullyQualify("💙"):  "heartblue",

	// Page 1
	variationselector.FullyQualify("🙂"):  "smile",
	variationselector.FullyQualify("😄"):  "laugh",
	variationselector.FullyQualify("❤️"): "heart",
	variationselector.FullyQualify("😘"):  "kiss",
	variationselector.FullyQualify("☹️"): "sad",
	variationselector.FullyQualify("😛"):  "tongueout",
	variationselector.FullyQualify("😉"):  "wink",
	variationselector.FullyQualify("😢"):  "cry",
	variationselector.FullyQualify("😍"):  "inlove",
	variationselector.FullyQualify("🤗"):  "hug",
	variationselector.FullyQualify("😂"):  "cwl",
	variationselector.FullyQualify("💋"):  "lips",

	// Page 2
	variationselector.FullyQualify("😊"):  "blush",
	variationselector.FullyQualify("😮"):  "surprised",
	variationselector.FullyQualify("🐧"):  "penguin",
	variationselector.FullyQualify("👍"):  "like",
	variationselector.FullyQualify("😎"):  "cool",
	variationselector.FullyQualify("🤣"):  "rofl",
	variationselector.FullyQualify("🐱"):  "cat",
	variationselector.FullyQualify("🐵"):  "monkey",
	variationselector.FullyQualify("👋"):  "hi",
	variationselector.FullyQualify("❄️"): "snowangel",
	variationselector.FullyQualify("🌸"):  "flower",
	variationselector.FullyQualify("😁"):  "giggle",
	variationselector.FullyQualify("😈"):  "devil",
	variationselector.FullyQualify("🥳"):  "party",

	// Page 3
	variationselector.FullyQualify("😟"):    "worry",
	variationselector.FullyQualify("🍾"):    "champagne",
	variationselector.FullyQualify("☀️"):   "sun",
	variationselector.FullyQualify("⭐"):    "star",
	variationselector.FullyQualify("🐻‍❄️"): "polarbear",
	variationselector.FullyQualify("🙄"):    "eyeroll",
	variationselector.FullyQualify("😶"):    "speechless",
	variationselector.FullyQualify("🤔"):    "wonder",
	variationselector.FullyQualify("😠"):    "angry",
	variationselector.FullyQualify("🤮"):    "puke",
	variationselector.FullyQualify("🤦"):    "facepalm",
	variationselector.FullyQualify("😓"):    "sweat",
	variationselector.FullyQualify("🤡"):    "holidayspirit",
	variationselector.FullyQualify("😴"):    "sleepy",

	// Page 4
	variationselector.FullyQualify("🙇"): "bow",
	variationselector.FullyQualify("💄"): "makeup",
	variationselector.FullyQualify("💵"): "cash",
	variationselector.FullyQualify("🤐"): "lipssealed",
	variationselector.FullyQualify("🥶"): "shivering",
	variationselector.FullyQualify("🎂"): "cake",
	variationselector.FullyQualify("🤕"): "headbang",
	variationselector.FullyQualify("💃"): "dance",
	variationselector.FullyQualify("😳"): "wasntme",
	variationselector.FullyQualify("🤢"): "hungover",
	variationselector.FullyQualify("🥱"): "yawn",
	variationselector.FullyQualify("🎁"): "gift",
	variationselector.FullyQualify("😇"): "angel",
	variationselector.FullyQualify("🎄"): "xmastree",

	// Page 5
	variationselector.FullyQualify("💔"): "brokenheart",
	variationselector.FullyQualify("🤔"): "think",
	variationselector.FullyQualify("👏"): "clap",
	variationselector.FullyQualify("👊"): "punch",
	variationselector.FullyQualify("😒"): "envy",
	variationselector.FullyQualify("🤝"): "handshake",
	variationselector.FullyQualify("🙂"): "nod",
	variationselector.FullyQualify("🤓"): "nerdy",
	variationselector.FullyQualify("🖤"): "emo",
	variationselector.FullyQualify("💪"): "muscle",
	variationselector.FullyQualify("😋"): "mmm",
	variationselector.FullyQualify("🙌"): "highfive",
	variationselector.FullyQualify("🦃"): "turkey",
	variationselector.FullyQualify("📞"): "call",

	// Page 6
	variationselector.FullyQualify("🧔"):  "movember",
	variationselector.FullyQualify("🐶"):  "dog",
	variationselector.FullyQualify("☕"):  "coffee",
	variationselector.FullyQualify("👉"):  "poke",
	variationselector.FullyQualify("🤬"):  "swear",
	variationselector.FullyQualify("😑"):  "donttalktome",
	variationselector.FullyQualify("🤞"):  "fingerscrossed",
	variationselector.FullyQualify("🌈"):  "rainbow",
	variationselector.FullyQualify("🎧"):  "headphones",
	variationselector.FullyQualify("⏳"):  "waiting",
	variationselector.FullyQualify("🎉"):  "festiveparty",
	variationselector.FullyQualify("🥷"):  "bandit",
	variationselector.FullyQualify("🐿️"): "heidy",
	variationselector.FullyQualify("🍺"):  "beer",

	// Page 7
	variationselector.FullyQualify("🤦‍♂️"): "doh",
	variationselector.FullyQualify("💣"):    "bomb",
	variationselector.FullyQualify("😀"):    "happy",
	variationselector.FullyQualify("🥷"):    "ninja",
}

var emotionKeyToEmoji = func() map[string]string {
	inverse := make(map[string]string, len(emojiToEmotionKey))
	for emoji, key := range emojiToEmotionKey {
		if _, exists := inverse[key]; !exists {
			inverse[key] = emoji
		}
	}
	return inverse
}()

func MapEmojiToEmotionKey(emoji string) (string, bool) {
	if strings.TrimSpace(emoji) == "" {
		return "", false
	}
	normalized := variationselector.FullyQualify(emoji)
	key, ok := emojiToEmotionKey[normalized]
	if ok {
		return key, true
	}
	// Passthrough: if it looks like a Unicode emoji (non-ASCII, short), use it directly.
	if IsUnicodeEmoji(normalized) {
		return normalized, true
	}
	return "", false
}

func MapEmotionKeyToEmoji(emotionKey string) (string, bool) {
	emotionKey = strings.TrimSpace(emotionKey)
	if emotionKey == "" {
		return "", false
	}
	emoji, ok := emotionKeyToEmoji[emotionKey]
	if ok {
		return emoji, true
	}
	// Passthrough: if the emotion key looks like a Unicode emoji, use it as-is.
	if IsUnicodeEmoji(emotionKey) {
		return emotionKey, true
	}
	// Custom org emoji: use the key as a shortcode-style text.
	if len(emotionKey) > 0 {
		return ":" + emotionKey + ":", true
	}
	return "", false
}

// IsUnicodeEmoji returns true if s appears to be a Unicode emoji character(s).
// It checks that the string is non-ASCII and short (≤20 bytes, typical for emoji sequences).
func IsUnicodeEmoji(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < 0x80 {
			// Allow variation selectors and zero-width joiners
			if r != 0x20 {
				return false
			}
		}
	}
	return true
}

func NormalizeTeamsReactionMessageID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "msg/") {
		return strings.TrimPrefix(value, "msg/")
	}
	return value
}

func NormalizeTeamsReactionTargetMessageID(value string) string {
	return NormalizeTeamsReactionMessageID(value)
}
