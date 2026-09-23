package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

type reproResolver struct{}

func (reproResolver) resolveGhostMXID(_ context.Context, teamsUserID string) id.UserID {
	return id.UserID("@msteams_" + strings.ReplaceAll(teamsUserID, ":", "=3a") + ":localhost")
}
func (reproResolver) parseGhostMXID(id.UserID) (networkid.UserID, bool) { return "", false }

func TestMentionSplitAcrossSpans(t *testing.T) {
	raw := `<p>Let's go, the king is BACK <span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="0">Ethan</span>&nbsp;<span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="1">Patrick</span>&nbsp;<span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="2">Santee</span></p>`
	props := []byte(`{"mentions":"[{\"@type\":\"http://schema.skype.com/Mention\",\"itemid\":0,\"mri\":\"8:orgid:5c2decab\",\"mentionType\":\"person\",\"displayName\":\"Ethan\"},{\"@type\":\"http://schema.skype.com/Mention\",\"itemid\":1,\"mri\":\"8:orgid:5c2decab\",\"mentionType\":\"person\",\"displayName\":\"Patrick\"},{\"@type\":\"http://schema.skype.com/Mention\",\"itemid\":2,\"mri\":\"8:orgid:5c2decab\",\"mentionType\":\"person\",\"displayName\":\"Santee\"}]","formatVariant":"TEAMS"}`)
	mentions := model.ResolveMentions(model.ParseMentionsFromHTML(raw), model.ExtractMentionMRIs(props))
	t.Logf("mentions: %+v", mentions)
	content := model.NormalizeMessageBody(raw)
	parts := []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: content.Body, Format: event.FormatHTML, FormattedBody: content.FormattedBody}}}
	applyMentionPillsWithResolver(context.Background(), parts, mentions, reproResolver{})
	t.Logf("body=%q\nfb=%q", parts[0].Content.Body, parts[0].Content.FormattedBody)
	fb := parts[0].Content.FormattedBody
	if strings.Count(fb, "matrix.to/#/@msteams_8=3aorgid=3a5c2decab:localhost") != 1 {
		t.Errorf("want exactly one pill, got %q", fb)
	}
	if !strings.Contains(fb, ">@Ethan\u00a0Patrick\u00a0Santee</a>") {
		t.Errorf("pill should carry the whole name, got %q", fb)
	}
}

func TestWithSubject(t *testing.T) {
	msg := withSubject(model.RemoteMessage{Body: "body text", FormattedBody: "<p>body text</p>", PropertiesRaw: []byte(`{"subject":"We've done it"}`)})
	if msg.Body != "We've done it\n\nbody text" {
		t.Errorf("body = %q", msg.Body)
	}
	if msg.FormattedBody != "<strong>We&#39;ve done it</strong><br><br><p>body text</p>" {
		t.Errorf("formatted = %q", msg.FormattedBody)
	}
	imageOnly := withSubject(model.RemoteMessage{PropertiesRaw: []byte(`{"subject":"Title"}`)})
	if imageOnly.Body != "Title" || imageOnly.FormattedBody != "<strong>Title</strong>" {
		t.Errorf("image-only post: body=%q formatted=%q", imageOnly.Body, imageOnly.FormattedBody)
	}
	plain := withSubject(model.RemoteMessage{Body: "hi", PropertiesRaw: []byte(`{"mentions":"[]"}`)})
	if plain.Body != "hi" || plain.FormattedBody != "" {
		t.Errorf("no subject should change nothing: %+v", plain)
	}
}

func TestTableSurvivesNormalisation(t *testing.T) {
	c := &TeamsClient{}
	cm, err := c.convertTeamsMessage(context.Background(), nil, nil, model.RemoteMessage{
		MessageID:     "1",
		MessageType:   "RichText/Html",
		Body:          model.NormalizeMessageBody("<table><tbody><tr><td>76.19%</td><td>x</td></tr></tbody></table>").Body,
		FormattedBody: model.NormalizeMessageBody("<table><tbody><tr><td>76.19%</td><td>x</td></tr></tbody></table>").FormattedBody,
	})
	if err != nil || cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("cm = %+v, err = %v", cm, err)
	}
	if fb := cm.Parts[0].Content.FormattedBody; !strings.Contains(fb, "<table>") || !strings.Contains(fb, "<td>76.19%</td>") {
		t.Errorf("table lost: %q", fb)
	}
}
