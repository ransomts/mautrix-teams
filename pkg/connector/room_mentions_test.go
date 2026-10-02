package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func htmlPart(formatted string) []*bridgev2.ConvertedMessagePart {
	return []*bridgev2.ConvertedMessagePart{{
		Type:    event.EventMessage,
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "x", Format: event.FormatHTML, FormattedBody: formatted},
	}}
}

// The message as Teams sent it (a group chat, 2026-09).
func TestInboundEveryoneMention(t *testing.T) {
	raw := `<p><span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="0">Everyone</span>&nbsp;hey don't put the last slide up</p>`
	props := []byte(`{"mentions":"[{\"@type\":\"http://schema.skype.com/Mention\",\"itemid\":0,\"mri\":\"19:28c8b4261c3541dca2e3533b3b7fdd25@thread.v2\",\"mentionType\":\"everyone\",\"displayName\":\"Everyone\"}]"}`)
	mentions := model.ResolveMentions(model.ParseMentionsFromHTML(raw), model.ExtractMentionMRIs(props))
	content := model.NormalizeMessageBody(raw)
	self := mentionSelf{teamsID: "8:orgid:me", mxid: "@me:localhost"}

	parts := htmlPart(content.FormattedBody)
	applyMentionsWithResolver(context.Background(), parts, mentions, reproResolver{}, self)
	got := parts[0].Content
	if want := "<p>@Everyone\u00a0hey don&#39;t put the last slide up</p>"; got.FormattedBody != want {
		t.Errorf("formatted body = %q, want %q", got.FormattedBody, want)
	}
	if got.Mentions == nil || !got.Mentions.Room || !got.Mentions.Has("@me:localhost") {
		t.Errorf("mentions = %+v, want the room and the user", got.Mentions)
	}

	// The user's own @Everyone mentions the room but not them.
	self.isSender = true
	parts = htmlPart(content.FormattedBody)
	applyMentionsWithResolver(context.Background(), parts, mentions, reproResolver{}, self)
	if m := parts[0].Content.Mentions; m == nil || !m.Room || len(m.UserIDs) != 0 {
		t.Errorf("own message mentions = %+v, want the room only", m)
	}
}

func TestInboundPillNotNested(t *testing.T) {
	resolver := &mockGhostResolver{teamsToMXID: map[string]id.UserID{
		"8:orgid:alex":  "@teams_alex:example.org",
		"8:orgid:alexs": "@teams_alexs:example.org",
	}}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:alex", DisplayName: "Alex", ItemID: "0"},
		{UserID: "8:orgid:alexs", DisplayName: "Alex Smith", ItemID: "2"},
	}
	parts := htmlPart("Hey Alex and Alex Smith")
	applyMentionPillsWithResolver(context.Background(), parts, mentions, resolver)
	want := `Hey <a href="https://matrix.to/#/@teams_alex:example.org">@Alex</a> and <a href="https://matrix.to/#/@teams_alexs:example.org">@Alex Smith</a>`
	if got := parts[0].Content.FormattedBody; got != want {
		t.Errorf("formatted body =\n %q\nwant\n %q", got, want)
	}
	if m := parts[0].Content.Mentions; m == nil || !m.Has("@teams_alex:example.org") || !m.Has("@teams_alexs:example.org") || m.Room {
		t.Errorf("mentions = %+v", m)
	}
}

func TestInboundSelfMentionPillsMatrixUser(t *testing.T) {
	mentions := []model.TeamsMention{{UserID: "8:orgid:me", DisplayName: "Tim", ItemID: "0"}}
	parts := htmlPart("Thanks Tim")
	applyMentionsWithResolver(context.Background(), parts, mentions, reproResolver{}, mentionSelf{teamsID: "8:orgid:me", mxid: "@me:localhost"})
	if got, want := parts[0].Content.FormattedBody, `Thanks <a href="https://matrix.to/#/@me:localhost">@Tim</a>`; got != want {
		t.Errorf("formatted body = %q, want %q", got, want)
	}
	if m := parts[0].Content.Mentions; m == nil || !m.Has("@me:localhost") {
		t.Errorf("mentions = %+v", m)
	}
}

func TestOutboundRoomMention(t *testing.T) {
	const group = "19:28c8b4261c3541dca2e3533b3b7fdd25@thread.v2"
	person := []map[string]any{{"id": 0, "mri": "8:orgid:a", "displayName": "A"}}

	body, mentions := convertRoomMentionToTeams("<p>@room hi, mail a@room.org</p>", person, group)
	if want := `<p><span itemtype="http://schema.skype.com/Mention" itemscope="" itemid="1">Everyone</span> hi, mail a@room.org</p>`; body != want {
		t.Errorf("body = %q", body)
	}
	if len(mentions) != 2 || mentions[1]["mri"] != group || mentions[1]["mentionType"] != "everyone" || mentions[1]["itemid"] != 1 {
		t.Errorf("mentions = %+v", mentions)
	}

	for _, thread := range []string{
		"19:a_b@unq.gbl.spaces", // one-to-one: no @Everyone
		"19:abc@thread.tacv2",   // channel: @channel is not supported
		"48:notes",
	} {
		if body, mentions := convertRoomMentionToTeams("<p>@room hi</p>", nil, thread); body != "<p>@room hi</p>" || mentions != nil {
			t.Errorf("%s: body = %q mentions = %+v, want unchanged", thread, body, mentions)
		}
	}
}
