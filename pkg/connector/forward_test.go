package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

func TestForwardHeader(t *testing.T) {
	c, _ := newBridgeTestClient(t, nil, &capturingEventSink{})
	ctx := context.Background()
	if err := c.Main.DB.Profile.Upsert(ctx, "8:orgid:author", "Ann Author", time.Now()); err != nil {
		t.Fatal(err)
	}
	insertTestPortal(t, c, &database.Portal{PortalKey: c.portalKey("19:source@thread.v2"), Name: "Group: Course staff"})

	// As the Teams client forwards a message (2026-09).
	raw := "<blockquote itemtype=\"http://schema.skype.com/Forward\">\n<p>here are the answer keys</p>\n</blockquote>"
	content := model.NormalizeMessageBody(raw)
	msg := model.RemoteMessage{
		MessageID:     "1",
		SenderID:      "8:orgid:me",
		MessageType:   "RichText/Html",
		Body:          content.Body,
		FormattedBody: content.FormattedBody,
		PropertiesRaw: []byte(`{"forwardTemplateId":"basic_forward_message_template","originalMessageContext":{"originalSender":"8:orgid:author","originalSentTime":"2025-09-10T15:55:56.042Z","originalThreadId":"19:source@thread.v2"}}`),
	}
	cm, err := c.convertTeamsMessage(ctx, nil, nil, msg)
	if err != nil || cm == nil || len(cm.Parts) != 1 {
		t.Fatalf("convert: %+v %v", cm, err)
	}
	got := cm.Parts[0].Content
	const header = "Forwarded from Ann Author · Group: Course staff · Sep 10, 2025"
	if !strings.HasPrefix(got.Body, header+"\n") {
		t.Errorf("body = %q", got.Body)
	}
	if !strings.HasPrefix(got.FormattedBody, "<p><em>"+header+"</em></p><blockquote>") || !strings.Contains(got.FormattedBody, "here are the answer keys") {
		t.Errorf("formatted body = %q", got.FormattedBody)
	}

	// Forwarded within the same chat: the chat is not named; an unknown
	// author is "someone".
	msg.PropertiesRaw = []byte(`{"originalMessageContext":{"originalSender":"8:orgid:nobody","originalThreadId":"19:source@thread.v2"}}`)
	portal := newTestPortal(networkid.PortalID("19:source@thread.v2"), "!room:test")
	cm, _ = c.convertTeamsMessage(ctx, portal, nil, msg)
	if body := cm.Parts[0].Content.Body; !strings.HasPrefix(body, "Forwarded from someone\n") {
		t.Errorf("body = %q", body)
	}

	// Not forwarded: untouched.
	msg.PropertiesRaw = nil
	cm, _ = c.convertTeamsMessage(ctx, nil, nil, msg)
	if strings.Contains(cm.Parts[0].Content.Body, "Forwarded") {
		t.Errorf("body = %q", cm.Parts[0].Content.Body)
	}
}
