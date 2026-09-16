package connector

import (
	"strconv"
	"strings"

	"go.mau.fi/util/variationselector"
)

// emotionKeyAliases maps inbound-only Teams reaction keys (ones whose emoji
// already maps to a different key in emojiToEmotionKey, or that Teams names
// without a codepoint prefix) to emoji.
var emotionKeyAliases = map[string]string{
	"yes":                 "👍",
	"no":                  "👎",
	"smileeyes":           "😊",
	"handsinair":          "🙌",
	"clappinghands":       "👏",
	"snake":               "🐍",
	"smilingfacewithtear": "🥲",
	"pinchedfingers":      "🤌",
	"eyes":                "👀",
	"heavycheckmark":      "✔️",
	"hundredpointssymbol": "💯",
	"laughcry":            "😂",
}

var skinToneSuffixes = map[string]string{
	"tone1": "\U0001F3FB",
	"tone2": "\U0001F3FC",
	"tone3": "\U0001F3FD",
	"tone4": "\U0001F3FE",
	"tone5": "\U0001F3FF",
}

// splitSkinTone separates a "-toneN" suffix from a Teams reaction key.
func splitSkinTone(key string) (string, string) {
	if idx := strings.LastIndex(key, "-tone"); idx > 0 {
		if tone, ok := skinToneSuffixes[key[idx+1:]]; ok {
			return key[:idx], tone
		}
	}
	return key, ""
}

// emojiFromCodepointKey decodes Teams' codepoint-prefixed reaction keys such
// as "2757_heavyexclamationmarksymbol" or "1f468_200d_1f4bb_mantechnologist":
// the leading underscore-separated hex tokens are the emoji's codepoints and
// the trailing token is its name.
func emojiFromCodepointKey(key string) (string, bool) {
	parts := strings.Split(key, "_")
	var runes []rune
	for _, part := range parts {
		if len(part) < 2 || len(part) > 6 {
			break
		}
		value, err := strconv.ParseUint(part, 16, 32)
		if err != nil || value < 0x20 || value > 0x10FFFF {
			break
		}
		runes = append(runes, rune(value))
	}
	// Require a trailing name so hex-looking words ("beef") are not decoded.
	if len(runes) == 0 || len(runes) == len(parts) {
		return "", false
	}
	return variationselector.FullyQualify(string(runes)), true
}

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
	base, tone := splitSkinTone(emotionKey)
	if emoji, ok := emotionKeyToEmoji[base]; ok {
		return emoji + tone, true
	}
	if emoji, ok := emotionKeyAliases[base]; ok {
		return variationselector.FullyQualify(emoji) + tone, true
	}
	if emoji, ok := emojiFromCodepointKey(base); ok {
		return emoji + tone, true
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
