package connector

// Forwarded messages.
//
// The Teams client forwards a message as a new one whose content is the
// original in a <blockquote itemtype="http://schema.skype.com/Forward">,
// with properties.originalMessageContext naming the original author, time
// and chat.  The blockquote survives conversion; the attribution is added
// above it as a line of its own.

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// applyForwardHeader adds "Forwarded from <author> · <chat> · <date>" to
// the first text part of cm when msg was forwarded.  The chat is named only
// when it is not the one the message is in.
func (c *TeamsClient) applyForwardHeader(ctx context.Context, portal *bridgev2.Portal, cm *bridgev2.ConvertedMessage, msg model.RemoteMessage) {
	fwd, ok := model.ExtractForwardContext(msg.PropertiesRaw)
	if !ok || cm == nil {
		return
	}
	var part *bridgev2.ConvertedMessagePart
	for _, p := range cm.Parts {
		if p != nil && p.Content != nil && (p.Content.MsgType == event.MsgText || p.Content.MsgType == event.MsgNotice) {
			part = p
			break
		}
	}
	if part == nil {
		return
	}
	fields := []string{"Forwarded from " + c.forwardAuthorName(ctx, fwd.OriginalSender)}
	if fwd.OriginalThreadID != "" && (portal == nil || string(portal.ID) != fwd.OriginalThreadID) {
		if name := c.forwardChatName(ctx, fwd.OriginalThreadID); name != "" {
			fields = append(fields, name)
		}
	}
	if !fwd.OriginalSentTime.IsZero() {
		fields = append(fields, forwardDate(fwd.OriginalSentTime, time.Now()))
	}
	header := strings.Join(fields, " · ")

	content := part.Content
	if content.FormattedBody == "" {
		content.FormattedBody = strings.ReplaceAll(html.EscapeString(content.Body), "\n", "<br>")
	}
	content.Format = event.FormatHTML
	content.FormattedBody = "<p><em>" + html.EscapeString(header) + "</em></p>" + content.FormattedBody
	content.Body = header + "\n" + content.Body
}

// forwardAuthorName names a forwarded message's author: their saved Teams
// profile name, else their ghost's, else "someone".
func (c *TeamsClient) forwardAuthorName(ctx context.Context, mri string) string {
	if mri == "" || c == nil || c.Main == nil {
		return "someone"
	}
	if c.Main.DB != nil {
		if profile, err := c.Main.DB.Profile.GetByTeamsUserID(ctx, mri); err == nil && profile != nil && strings.TrimSpace(profile.DisplayName) != "" {
			return strings.TrimSpace(profile.DisplayName)
		}
	}
	if c.Main.Bridge != nil && c.Main.Bridge.DB != nil {
		if ghost, err := c.Main.Bridge.DB.Ghost.GetByID(ctx, networkid.UserID(mri)); err == nil && ghost != nil && ghost.Name != "" {
			return ghost.Name
		}
	}
	return "someone"
}

// forwardChatName is the bridged room name of Teams thread threadID, or ""
// when the bridge has no room for it.
func (c *TeamsClient) forwardChatName(ctx context.Context, threadID string) string {
	if c == nil || c.Login == nil || c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return ""
	}
	portal, err := c.Main.Bridge.DB.Portal.GetByKey(ctx, c.portalKey(threadID))
	if err != nil || portal == nil {
		return ""
	}
	return strings.TrimSpace(portal.Name)
}

// forwardDate is t as "Sep 10", with the year when it is not now's.
func forwardDate(t, now time.Time) string {
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return fmt.Sprintf("%s, %d", t.Format("Jan 2"), t.Year())
}
