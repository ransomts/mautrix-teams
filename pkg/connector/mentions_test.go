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

// mockGhostResolver implements ghostResolver for testing.
type mockGhostResolver struct {
	// teamsToMXID maps Teams user ID -> Matrix user ID.
	teamsToMXID map[string]id.UserID
	// mxidToNetwork maps Matrix user ID -> network user ID.
	mxidToNetwork map[id.UserID]networkid.UserID
}

func (m *mockGhostResolver) resolveGhostMXID(ctx context.Context, teamsUserID string) id.UserID {
	if m == nil || m.teamsToMXID == nil {
		return ""
	}
	return m.teamsToMXID[teamsUserID]
}

func (m *mockGhostResolver) parseGhostMXID(mxid id.UserID) (networkid.UserID, bool) {
	if m == nil || m.mxidToNetwork == nil {
		return "", false
	}
	nid, ok := m.mxidToNetwork[mxid]
	return nid, ok
}

func TestApplyMentionPillsFormattedBody(t *testing.T) {
	resolver := &mockGhostResolver{
		teamsToMXID: map[string]id.UserID{
			"8:orgid:alice-uuid": "@teams_alice:example.org",
		},
	}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:alice-uuid", DisplayName: "Alice", ItemID: "0"},
	}
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          "Hey Alice, check this out",
			Format:        event.FormatHTML,
			FormattedBody: "Hey Alice, check this out",
		},
	}}

	applyMentionPillsWithResolver(context.Background(), parts, mentions, resolver)

	got := parts[0].Content.FormattedBody
	if !strings.Contains(got, `<a href="https://matrix.to/#/@teams_alice:example.org">@Alice</a>`) {
		t.Fatalf("expected mention pill in formatted body, got %q", got)
	}
	if !strings.Contains(got, "Hey ") {
		t.Fatalf("expected surrounding text preserved, got %q", got)
	}
}

func TestApplyMentionPillsPlainBodyOnly(t *testing.T) {
	resolver := &mockGhostResolver{
		teamsToMXID: map[string]id.UserID{
			"8:orgid:bob-uuid": "@teams_bob:example.org",
		},
	}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:bob-uuid", DisplayName: "Bob", ItemID: "0"},
	}
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "Hello Bob",
		},
	}}

	applyMentionPillsWithResolver(context.Background(), parts, mentions, resolver)

	if parts[0].Content.Format != event.FormatHTML {
		t.Fatalf("expected HTML format to be set, got %q", parts[0].Content.Format)
	}
	got := parts[0].Content.FormattedBody
	if !strings.Contains(got, `<a href="https://matrix.to/#/@teams_bob:example.org">@Bob</a>`) {
		t.Fatalf("expected mention pill in new formatted body, got %q", got)
	}
}

func TestApplyMentionPillsMultipleMentions(t *testing.T) {
	resolver := &mockGhostResolver{
		teamsToMXID: map[string]id.UserID{
			"8:orgid:alice": "@teams_alice:example.org",
			"8:orgid:bob":   "@teams_bob:example.org",
		},
	}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:alice", DisplayName: "Alice", ItemID: "0"},
		{UserID: "8:orgid:bob", DisplayName: "Bob", ItemID: "1"},
	}
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          "Alice and Bob",
			Format:        event.FormatHTML,
			FormattedBody: "Alice and Bob",
		},
	}}

	applyMentionPillsWithResolver(context.Background(), parts, mentions, resolver)

	got := parts[0].Content.FormattedBody
	if !strings.Contains(got, "@teams_alice:example.org") {
		t.Fatalf("expected Alice pill, got %q", got)
	}
	if !strings.Contains(got, "@teams_bob:example.org") {
		t.Fatalf("expected Bob pill, got %q", got)
	}
}

func TestApplyMentionPillsSkipsUnresolvable(t *testing.T) {
	resolver := &mockGhostResolver{
		teamsToMXID: map[string]id.UserID{},
	}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:unknown", DisplayName: "Unknown", ItemID: "0"},
	}
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          "Hey Unknown",
			Format:        event.FormatHTML,
			FormattedBody: "Hey Unknown",
		},
	}}

	applyMentionPillsWithResolver(context.Background(), parts, mentions, resolver)

	got := parts[0].Content.FormattedBody
	if strings.Contains(got, "matrix.to") {
		t.Fatalf("expected no pill for unresolvable user, got %q", got)
	}
	if got != "Hey Unknown" {
		t.Fatalf("expected body unchanged, got %q", got)
	}
}

func TestApplyMentionPillsNilResolver(t *testing.T) {
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          "Hey Alice",
			Format:        event.FormatHTML,
			FormattedBody: "Hey Alice",
		},
	}}
	mentions := []model.TeamsMention{
		{UserID: "8:orgid:alice", DisplayName: "Alice", ItemID: "0"},
	}

	applyMentionPillsWithResolver(context.Background(), parts, mentions, nil)

	if parts[0].Content.FormattedBody != "Hey Alice" {
		t.Fatalf("expected no change with nil resolver, got %q", parts[0].Content.FormattedBody)
	}
}

func TestApplyMentionPillsEmptyMentions(t *testing.T) {
	parts := []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "no mentions",
		},
	}}

	applyMentionPillsWithResolver(context.Background(), parts, nil, &mockGhostResolver{})

	if parts[0].Content.FormattedBody != "" {
		t.Fatalf("expected no formatted body, got %q", parts[0].Content.FormattedBody)
	}
}

func TestConvertMatrixMentionsToTeamsSingleMention(t *testing.T) {
	resolver := &mockGhostResolver{
		mxidToNetwork: map[id.UserID]networkid.UserID{
			"@teams_alice:example.org": "8:orgid:alice-uuid",
		},
	}
	input := `Hello <a href="https://matrix.to/#/@teams_alice:example.org">Alice</a>!`

	got, mentions := convertMatrixMentionsToTeamsWithResolver(input, resolver)

	if len(mentions) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(mentions))
	}
	if mentions[0]["mri"] != "8:orgid:alice-uuid" {
		t.Fatalf("unexpected MRI: %v", mentions[0]["mri"])
	}
	if mentions[0]["displayName"] != "Alice" {
		t.Fatalf("unexpected displayName: %v", mentions[0]["displayName"])
	}
	if mentions[0]["id"] != 0 {
		t.Fatalf("unexpected id: %v", mentions[0]["id"])
	}
	if !strings.Contains(got, `<span itemtype="http://schema.skype.com/Mention" itemid="0">@Alice</span>`) {
		t.Fatalf("expected Teams mention span, got %q", got)
	}
	if !strings.HasPrefix(got, "Hello ") || !strings.HasSuffix(got, "!") {
		t.Fatalf("expected surrounding text preserved, got %q", got)
	}
}

func TestConvertMatrixMentionsToTeamsMultipleMentions(t *testing.T) {
	resolver := &mockGhostResolver{
		mxidToNetwork: map[id.UserID]networkid.UserID{
			"@teams_alice:example.org": "8:orgid:alice",
			"@teams_bob:example.org":   "8:orgid:bob",
		},
	}
	input := `<a href="https://matrix.to/#/@teams_alice:example.org">Alice</a> and <a href="https://matrix.to/#/@teams_bob:example.org">Bob</a>`

	got, mentions := convertMatrixMentionsToTeamsWithResolver(input, resolver)

	if len(mentions) != 2 {
		t.Fatalf("expected 2 mentions, got %d", len(mentions))
	}
	if mentions[0]["id"] != 0 || mentions[1]["id"] != 1 {
		t.Fatalf("unexpected mention IDs: %v, %v", mentions[0]["id"], mentions[1]["id"])
	}
	if !strings.Contains(got, `itemid="0">@Alice</span>`) {
		t.Fatalf("expected Alice span, got %q", got)
	}
	if !strings.Contains(got, `itemid="1">@Bob</span>`) {
		t.Fatalf("expected Bob span, got %q", got)
	}
}

func TestConvertMatrixMentionsToTeamsNoMentions(t *testing.T) {
	resolver := &mockGhostResolver{}
	input := "just plain text"

	got, mentions := convertMatrixMentionsToTeamsWithResolver(input, resolver)

	if got != input {
		t.Fatalf("expected unchanged body, got %q", got)
	}
	if mentions != nil {
		t.Fatalf("expected nil mentions, got %#v", mentions)
	}
}

func TestConvertMatrixMentionsToTeamsNonGhostPill(t *testing.T) {
	resolver := &mockGhostResolver{
		mxidToNetwork: map[id.UserID]networkid.UserID{},
	}
	input := `Hey <a href="https://matrix.to/#/@realuser:example.org">Real User</a>`

	got, mentions := convertMatrixMentionsToTeamsWithResolver(input, resolver)

	if got != input {
		t.Fatalf("expected non-ghost pill left unchanged, got %q", got)
	}
	if mentions != nil {
		t.Fatalf("expected nil mentions for non-ghost, got %#v", mentions)
	}
}

func TestConvertMatrixMentionsToTeamsNilResolver(t *testing.T) {
	input := `<a href="https://matrix.to/#/@foo:bar">Foo</a>`
	got, mentions := convertMatrixMentionsToTeamsWithResolver(input, nil)
	if got != input {
		t.Fatalf("expected unchanged with nil resolver, got %q", got)
	}
	if mentions != nil {
		t.Fatalf("expected nil, got %#v", mentions)
	}
}

func TestWrapTeamsReplyHTML(t *testing.T) {
	got := wrapTeamsReplyHTML("1234567890", "Hello world")
	if !strings.Contains(got, `itemtype="http://schema.skype.com/Reply"`) {
		t.Fatalf("expected reply itemtype, got %q", got)
	}
	if !strings.Contains(got, `itemid="1234567890"`) {
		t.Fatalf("expected reply itemid, got %q", got)
	}
	if !strings.HasSuffix(got, "Hello world") {
		t.Fatalf("expected body after blockquote, got %q", got)
	}
}
