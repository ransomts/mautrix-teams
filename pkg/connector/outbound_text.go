package connector

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"maunium.net/go/mautrix/event"
)

// outboundMsgTypeSupported reports whether a Matrix message of type t can
// be sent to Teams.
func outboundMsgTypeSupported(t event.MessageType) bool {
	switch t {
	case event.MsgText, event.MsgNotice, event.MsgEmote, event.MsgLocation,
		event.MsgImage, event.MsgFile, event.MsgVideo, event.MsgAudio:
		return true
	}
	return false
}

// outboundTextHTML turns a text-like Matrix message (text, notice, emote,
// location) into Teams HTML and its mention properties.  A Matrix HTML
// message (ement's Org filter wraps even plain text in <p>) is already HTML
// and must not be escaped, or Teams shows the literal tags; plain text is
// escaped and wrapped.  Teams has no emotes, and shows the sender anyway, so
// "/me waves" goes as italic "waves"; a location goes as its text with an
// OpenStreetMap link.  A Matrix room mention ("@room") in a group chat
// becomes a Teams @Everyone.  New messages and edits both use this.
func (c *TeamsClient) outboundTextHTML(content *event.MessageEventContent, threadID string) (string, []map[string]any) {
	var body string
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		body = matrixHTMLToTeamsHTML(content.FormattedBody)
	} else {
		body = plaintextToTeamsHTML(content.Body)
	}
	switch content.MsgType {
	case event.MsgEmote:
		// Italicise inside a single paragraph rather than around it.
		if inner, ok := strings.CutPrefix(body, "<p>"); ok && strings.Count(body, "<p>") == 1 && strings.HasSuffix(inner, "</p>") {
			body = "<p><em>" + strings.TrimSuffix(inner, "</p>") + "</em></p>"
		} else {
			body = "<em>" + body + "</em>"
		}
	case event.MsgLocation:
		if link := geoURIToMapLink(content.GeoURI); link != "" {
			body += fmt.Sprintf(`<p><a href="%s">%s</a></p>`, html.EscapeString(link), html.EscapeString(link))
		}
	}
	body, mentions := c.convertMatrixMentionsToTeams(body)
	return convertRoomMentionToTeams(body, mentions, threadID)
}

// roomMentionRe matches "@room", Matrix's mention of the whole room, as a
// word of its own: not inside an address like "a@room.org" or "x@room".
var roomMentionRe = regexp.MustCompile(`(^|[^\w@./:-])@room\b`)

// teamsEveryoneSpan is the HTML the Teams client makes of an @Everyone.
const teamsEveryoneSpan = `<span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="%d">Everyone</span>`

// convertRoomMentionToTeams turns each "@room" in body into a Teams
// @Everyone, added to mentions, when threadID is a group chat (meeting
// chats included).  A one-to-one chat has no @Everyone, and a channel's
// equivalent (@channel) is left alone, so there "@room" stays text.  The
// mention entry copies what the Teams client sends: the chat's own thread
// ID as the MRI, mentionType "everyone".
func convertRoomMentionToTeams(body string, mentions []map[string]any, threadID string) (string, []map[string]any) {
	threadID = strings.TrimSpace(threadID)
	if !strings.HasSuffix(threadID, "@thread.v2") || !strings.Contains(body, "@room") {
		return body, mentions
	}
	next := len(mentions)
	out := roomMentionRe.ReplaceAllStringFunc(body, func(match string) string {
		prefix := strings.TrimSuffix(match, "@room")
		idx := next
		next++
		mentions = append(mentions, map[string]any{
			"@type":       "http://schema.skype.com/Mention",
			"id":          idx,
			"itemid":      idx,
			"mri":         threadID,
			"mentionType": "everyone",
			"displayName": "Everyone",
		})
		return prefix + fmt.Sprintf(teamsEveryoneSpan, idx)
	})
	return out, mentions
}

// geoURIToMapLink turns "geo:LAT,LON[;u=…]" into an OpenStreetMap link, or "".
func geoURIToMapLink(geoURI string) string {
	coords, ok := strings.CutPrefix(strings.TrimSpace(geoURI), "geo:")
	if !ok {
		return ""
	}
	coords, _, _ = strings.Cut(coords, ";")
	lat, lon, ok := strings.Cut(coords, ",")
	if !ok || lat == "" || lon == "" {
		return ""
	}
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%s&mlon=%s#map=16/%s/%s", lat, lon, lat, lon)
}

// truncateSnippet shortens s to at most limit bytes plus "...", cutting on
// a character boundary so no UTF-8 sequence is split.
func truncateSnippet(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
