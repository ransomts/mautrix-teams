package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	consumerclient "go.mau.fi/mautrix-teams/internal/teams/client"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// Thread IDs in the shapes Teams uses: a channel, a 1:1 chat (the two
// members' object IDs), and a group chat.
const (
	outChannelThread = "19:0123456789abcdef0123456789abcdef@thread.tacv2"
	outDMThread      = "19:00000001-0000-0000-0000-000000000001_00000002-0000-0000-0000-000000000002@unq.gbl.spaces"
	outGroupThread   = "19:0fedcba9876543210fedcba987654321@thread.v2"
)

// outboundAPI is mockTeamsAPI plus the arguments the outbound paths pass
// that mockTeamsAPI drops: the reply target, GetMessage's conversation,
// GIF sends, and an error for sends.
type outboundAPI struct {
	mockTeamsAPI

	omu        sync.Mutex
	replyTo    []string // replyToID of each SendFormattedMessage
	getMsgConv []string // conversationID of each GetMessage
	gifs       []sentGIF
	sendErr    error
}

type sentGIF struct{ ThreadID, URL, Title string }

func (a *outboundAPI) SendFormattedMessage(ctx context.Context, threadID, htmlContent, fromUserID, clientMessageID, replyToID string, mentions []map[string]any) (int, error) {
	a.omu.Lock()
	a.replyTo = append(a.replyTo, replyToID)
	err := a.sendErr
	a.omu.Unlock()
	if err != nil {
		return 0, err
	}
	return a.mockTeamsAPI.SendFormattedMessage(ctx, threadID, htmlContent, fromUserID, clientMessageID, replyToID, mentions)
}

func (a *outboundAPI) GetMessage(ctx context.Context, conversationID, messageID string) (*model.RemoteMessage, error) {
	a.omu.Lock()
	a.getMsgConv = append(a.getMsgConv, conversationID)
	a.omu.Unlock()
	return a.mockTeamsAPI.GetMessage(ctx, conversationID, messageID)
}

func (a *outboundAPI) SendGIFWithID(_ context.Context, threadID, gifURL, title, _, _ string) (int, error) {
	a.omu.Lock()
	defer a.omu.Unlock()
	a.gifs = append(a.gifs, sentGIF{threadID, gifURL, title})
	return 201, nil
}

// sent returns the single formatted send and its reply target.
func (a *outboundAPI) sent(t *testing.T) (sentMessage, string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.omu.Lock()
	defer a.omu.Unlock()
	if len(a.sentMessages) != 1 || len(a.replyTo) != 1 {
		t.Fatalf("want exactly one send, got %d (reply targets %v)", len(a.sentMessages), a.replyTo)
	}
	return a.sentMessages[0], a.replyTo[0]
}

func textMessage(portalID string, content *event.MessageEventContent) *bridgev2.MatrixMessage {
	return &bridgev2.MatrixMessage{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Event:   newTestEvent(),
			Content: content,
			Portal:  newTestPortal(networkid.PortalID(portalID), "!room:example.org"),
		},
	}
}

// isStatus reports whether err is the MessageStatus want (MessageStatus
// is not comparable, so errors.Is cannot tell).
func isStatus(err, want error) bool {
	var got, w bridgev2.MessageStatus
	return errors.As(err, &got) && errors.As(want, &w) && got.InternalError == w.InternalError
}

// pendingCount is the number of outgoing messages the portal is waiting
// to see echoed.
func pendingCount(p *bridgev2.Portal) int {
	return reflect.ValueOf(p).Elem().FieldByName("outgoingMessages").Len()
}

// ---------------------------------------------------------------------------
// Replies: channels thread, chats quote
// ---------------------------------------------------------------------------

func TestHandleMatrixMessageChannelReplyPostsIntoThread(t *testing.T) {
	cases := []struct {
		name    string
		replyTo *database.Message
		root    *database.Message
		want    string
	}{
		// Replying to a reply: the stored thread root, not the message.
		{"reply to reply", &database.Message{ID: "1726000000002", ThreadRoot: "1726000000001"}, nil, "1726000000001"},
		// Replying to the root post itself.
		{"reply to root", &database.Message{ID: "1726000000001"}, nil, "1726000000001"},
		// A Matrix thread message: the Matrix thread root wins.
		{"matrix thread", &database.Message{ID: "1726000000005", ThreadRoot: "1726000000004"}, &database.Message{ID: "1726000000003"}, "1726000000003"},
		// A new top-level post.
		{"top level", nil, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &outboundAPI{}
			c := newTestClient(nil, &capturingEventSink{})
			c.api = api
			msg := textMessage(outChannelThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "on it"})
			msg.ReplyTo = tc.replyTo
			msg.ThreadRoot = tc.root

			if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
				t.Fatalf("HandleMatrixMessage: %v", err)
			}
			sent, replyTo := api.sent(t)
			if replyTo != tc.want {
				t.Errorf("replyChainMessageId = %q, want %q", replyTo, tc.want)
			}
			// Channel replies are threaded, never quoted.
			if sent.Text != "<p>on it</p>" {
				t.Errorf("body = %q", sent.Text)
			}
			if len(api.getMsgConv) != 0 {
				t.Errorf("channel reply fetched the original: %v", api.getMsgConv)
			}
		})
	}
}

// The original of a chat quote-reply as the Teams client parses it from
// the messages endpoint: plain-text Body, HTML FormattedBody.
func quotedOriginal(id string) model.RemoteMessage {
	return model.RemoteMessage{
		MessageID:        id,
		SequenceID:       "41",
		SenderID:         "8:orgid:00000002-0000-0000-0000-000000000002",
		IMDisplayName:    "Ann Example",
		TokenDisplayName: "Ann Example",
		Body:             "Lunch at noon?",
		FormattedBody:    "<p>Lunch at noon?</p>",
		MessageType:      "RichText/Html",
		Timestamp:        time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestHandleMatrixMessageChatReplyQuotesOriginal(t *testing.T) {
	// The chat's API conversation differs from its thread ID; the original
	// must be fetched from the conversation (as with the self chat, whose
	// conversation is "48:notes").
	api := &outboundAPI{}
	api.messages = []model.RemoteMessage{quotedOriginal("1726000000001")}
	c := newSchedTestClient(t, api, &capturingEventSink{}, map[string]string{outGroupThread: "48:notes"})
	msg := textMessage(outGroupThread, &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "> <@ann:example.org> Lunch at noon?\n\nSure",
		Format:        event.FormatHTML,
		FormattedBody: `<mx-reply><blockquote>In reply to Lunch at noon?</blockquote></mx-reply>Sure`,
	})
	msg.ReplyTo = &database.Message{ID: "1726000000001"}

	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMatrixMessage: %v", err)
	}
	sent, replyTo := api.sent(t)
	if replyTo != "1726000000001" {
		t.Errorf("replyChainMessageId = %q", replyTo)
	}
	want := `<blockquote itemtype="http://schema.skype.com/Reply" itemid="1726000000001"><strong>Ann Example</strong><br>Lunch at noon?</blockquote>Sure`
	if sent.Text != want {
		t.Errorf("body:\n got %s\nwant %s", sent.Text, want)
	}
	if len(api.getMsgConv) != 1 || api.getMsgConv[0] != "48:notes" {
		t.Errorf("GetMessage conversations = %v, want [48:notes]", api.getMsgConv)
	}
}

func TestBuildTeamsReplyHTMLSnippets(t *testing.T) {
	long := strings.Repeat("a", 250)
	cases := []struct {
		name    string
		mutate  func(*model.RemoteMessage)
		wantHdr string
		wantTxt string
	}{
		{"token name when no im name", func(m *model.RemoteMessage) { m.IMDisplayName = "" }, "Ann Example", "Lunch at noon?"},
		{"formatted body when no body", func(m *model.RemoteMessage) { m.Body = "" }, "Ann Example", "&lt;p&gt;Lunch at noon?&lt;/p&gt;"},
		{"image only", func(m *model.RemoteMessage) {
			m.Body, m.FormattedBody = "", ""
			m.InlineImages = []model.TeamsInlineImage{{}}
		}, "Ann Example", "\U0001f4f7"},
		{"file only", func(m *model.RemoteMessage) {
			m.Body, m.FormattedBody = "", ""
			m.PropertiesFiles = `[{"fileName":"notes.pdf"}]`
		}, "Ann Example", "\U0001f4ce"},
		{"gif only", func(m *model.RemoteMessage) {
			m.Body, m.FormattedBody = "", ""
			m.GIFs = []model.TeamsGIF{{}}
		}, "Ann Example", "GIF"},
		{"escaped", func(m *model.RemoteMessage) {
			m.IMDisplayName = "Ann <Example>"
			m.Body = `if a < b && "c"`
		}, "Ann &lt;Example&gt;", "if a &lt; b &amp;&amp; &#34;c&#34;"},
		{"truncated", func(m *model.RemoteMessage) { m.Body = long }, "Ann Example", long[:200] + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := quotedOriginal("1726000000001")
			tc.mutate(&orig)
			api := &outboundAPI{}
			api.messages = []model.RemoteMessage{orig}
			c := newSchedTestClient(t, api, &capturingEventSink{}, nil)
			got := c.buildTeamsReplyHTML(context.Background(), outGroupThread, "1726000000001", "<p>x</p>")
			want := `<blockquote itemtype="http://schema.skype.com/Reply" itemid="1726000000001"><strong>` +
				tc.wantHdr + `</strong><br>` + tc.wantTxt + `</blockquote><p>x</p>`
			if got != want {
				t.Errorf("\n got %s\nwant %s", got, want)
			}
			// Without a thread state row the thread ID is the conversation.
			if len(api.getMsgConv) != 1 || api.getMsgConv[0] != outGroupThread {
				t.Errorf("GetMessage conversations = %v", api.getMsgConv)
			}
		})
	}
}

// BUG: the 200-byte snippet cut is by byte, so it can split a multi-byte
// character and put invalid UTF-8 in the quote (json.Marshal then sends
// U+FFFD).  This documents the current behaviour; see the report.
func TestBuildTeamsReplyHTMLTruncationSplitsRunes(t *testing.T) {
	orig := quotedOriginal("1726000000001")
	orig.Body = "a" + strings.Repeat("é", 150) // 301 bytes; byte 200 is mid-rune
	api := &outboundAPI{}
	api.messages = []model.RemoteMessage{orig}
	c := newSchedTestClient(t, api, &capturingEventSink{}, nil)
	got := c.buildTeamsReplyHTML(context.Background(), outGroupThread, "1726000000001", "")
	if utf8.ValidString(got) {
		t.Fatal("snippet truncation now keeps UTF-8 valid; the bug is fixed, update this test")
	}
}

func TestBuildTeamsReplyHTMLFallsBackToEmptyQuote(t *testing.T) {
	api := &outboundAPI{} // GetMessage finds nothing
	c := newSchedTestClient(t, api, &capturingEventSink{}, nil)
	got := c.buildTeamsReplyHTML(context.Background(), outDMThread, `id"x`, "<p>x</p>")
	want := `<blockquote itemtype="http://schema.skype.com/Reply" itemid="id&#34;x"></blockquote><p>x</p>`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Body conversion, mentions, GIFs, unsupported types
// ---------------------------------------------------------------------------

func TestHandleMatrixMessagePlainTextIsEscaped(t *testing.T) {
	api := &outboundAPI{}
	c := newTestClient(nil, &capturingEventSink{})
	c.api = api
	msg := textMessage(outDMThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "x < y & z\nnext"})
	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	sent, replyTo := api.sent(t)
	if sent.Text != "<p>x &lt; y &amp; z<br>next</p>" || replyTo != "" {
		t.Errorf("sent %q reply %q", sent.Text, replyTo)
	}
}

func TestHandleMatrixMessageConvertsMentionPills(t *testing.T) {
	api := &outboundAPI{}
	c, mx := newBridgeTestClient(t, api, &capturingEventSink{})
	ann := mx.ghostMXID("8:orgid:00000002-0000-0000-0000-000000000002")
	msg := textMessage(outGroupThread, &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "Ann Example: see @room and Bob",
		Format:        event.FormatHTML,
		FormattedBody: `<a href="https://matrix.to/#/` + string(ann) + `">Ann Example</a>: see <a href="https://matrix.to/#/@bob:example.org">Bob</a>`,
	})
	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	sent, _ := api.sent(t)
	wantBody := `<span itemtype="http://schema.skype.com/Mention" itemid="0">@Ann Example</span>: see <a href="https://matrix.to/#/@bob:example.org">Bob</a>`
	if sent.Text != wantBody {
		t.Errorf("body:\n got %s\nwant %s", sent.Text, wantBody)
	}
	if len(sent.Mentions) != 1 || sent.Mentions[0]["mri"] != "8:orgid:00000002-0000-0000-0000-000000000002" ||
		sent.Mentions[0]["displayName"] != "Ann Example" || sent.Mentions[0]["id"] != 0 {
		t.Errorf("mentions = %v", sent.Mentions)
	}
}

func TestHandleMatrixMessageSendsGIFByURL(t *testing.T) {
	api := &outboundAPI{}
	c := newTestClient(nil, &capturingEventSink{})
	c.api = api
	msg := textMessage(outDMThread, &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    "https://media.giphy.com/media/abc123/giphy.gif",
		Info:    &event.FileInfo{MimeType: "image/gif"},
	})
	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(api.gifs) != 1 || api.gifs[0].URL != "https://media.giphy.com/media/abc123/giphy.gif" || api.gifs[0].ThreadID != outDMThread {
		t.Fatalf("gif sends = %+v", api.gifs)
	}
}

// BUG (minor): an unsupported message type is refused after the pending
// echo was registered, and nothing removes it, so each refused event
// leaves an entry in the portal's outgoing-message map.
func TestHandleMatrixMessageUnsupportedTypes(t *testing.T) {
	for _, mt := range []event.MessageType{event.MsgEmote, event.MsgNotice, event.MsgLocation} {
		t.Run(string(mt), func(t *testing.T) {
			api := &outboundAPI{}
			c := newTestClient(nil, &capturingEventSink{})
			c.api = api
			msg := textMessage(outDMThread, &event.MessageEventContent{MsgType: mt, Body: "waves"})
			_, err := c.HandleMatrixMessage(context.Background(), msg)
			if !isStatus(err, bridgev2.ErrUnsupportedMessageType) {
				t.Fatalf("err = %v, want ErrUnsupportedMessageType", err)
			}
			if len(api.sentMessages) != 0 {
				t.Fatalf("sent %v", api.sentMessages)
			}
			if n := pendingCount(msg.Portal); n != 1 {
				t.Fatalf("pending entries = %d; the leak is fixed if 0, update this test", n)
			}
		})
	}
}

func TestHandleMatrixMessageAttachmentWithoutURLFails(t *testing.T) {
	api := &outboundAPI{}
	c := newTestClient(nil, &capturingEventSink{})
	c.api = api
	msg := textMessage(outDMThread, &event.MessageEventContent{MsgType: event.MsgFile, Body: "notes.pdf"})
	_, err := c.HandleMatrixMessage(context.Background(), msg)
	var status bridgev2.MessageStatus
	if !errors.As(err, &status) || !status.SendNotice {
		t.Fatalf("err = %#v, want a MessageStatus with a notice", err)
	}
	if n := pendingCount(msg.Portal); n != 0 {
		t.Fatalf("failed send left %d pending entries", n)
	}
	if len(api.sentMessages) != 0 {
		t.Fatalf("sent %v", api.sentMessages)
	}
}

// ---------------------------------------------------------------------------
// Send results
// ---------------------------------------------------------------------------

func TestHandleMatrixMessageSuccessWakesThreadAndRecordsEcho(t *testing.T) {
	api := &outboundAPI{}
	c := newTestClient(nil, &capturingEventSink{})
	c.api = api
	msg := textMessage(outDMThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "hi"})
	before := time.Now().UnixMilli()
	resp, err := c.HandleMatrixMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	sent, _ := api.sent(t)
	if string(resp.DB.ID) != sent.ClientMessageID || !resp.Pending || resp.StreamOrder < before {
		t.Errorf("response %+v for client message %s", resp, sent.ClientMessageID)
	}
	if string(resp.DB.SenderID) != testSelfUserID {
		t.Errorf("sender = %s", resp.DB.SenderID)
	}
	if n := pendingCount(msg.Portal); n != 1 {
		t.Errorf("pending entries = %d, want 1 (awaiting the echo)", n)
	}
	// The echo is recognised as ours, once.
	if !c.consumeSelfMessage(sent.ClientMessageID) || c.consumeSelfMessage(sent.ClientMessageID) {
		t.Error("self message not recorded exactly once")
	}
	// The poll loop is asked to fetch the echo now.
	select {
	case w := <-c.wakeChan():
		if w.threadID != outDMThread || w.receipts {
			t.Errorf("wakeup %+v", w)
		}
	default:
		t.Error("no poll requested for the thread")
	}
	if !c.threadIsActive(outDMThread, time.Now()) {
		t.Error("thread not marked active")
	}
}

func TestHandleMatrixMessageSendErrorsBecomeStatuses(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantCertain bool
		wantAsMsg   bool // the error text is the notice
		wantMessage string
	}{
		{"rejected", consumerclient.SendMessageError{Status: 403, BodySnippet: `{"errorCode":403}`}, true, true, ""},
		{"throttled", consumerclient.RetryableError{Status: 429, RetryAfter: 5 * time.Second}, false, false, "Teams API temporarily unavailable, please retry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &outboundAPI{sendErr: tc.err}
			c := newTestClient(nil, &capturingEventSink{})
			c.api = api
			msg := textMessage(outDMThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "hi"})
			_, err := c.HandleMatrixMessage(context.Background(), msg)
			var status bridgev2.MessageStatus
			if !errors.As(err, &status) {
				t.Fatalf("err %T is not a MessageStatus", err)
			}
			if status.IsCertain != tc.wantCertain || !status.SendNotice || status.Message != tc.wantMessage || status.ErrorAsMessage != tc.wantAsMsg ||
				status.ErrorReason != event.MessageStatusGenericError {
				t.Errorf("status %+v", status)
			}
			if !errors.Is(err, tc.err) {
				t.Error("status does not wrap the send error")
			}
			if n := pendingCount(msg.Portal); n != 0 {
				t.Errorf("failed send left %d pending entries", n)
			}
			if len(c.selfMessages) != 0 {
				t.Error("failed send recorded as own message")
			}
			select {
			case w := <-c.wakeChan():
				t.Errorf("failed send requested a poll: %+v", w)
			default:
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edits and deletes
// ---------------------------------------------------------------------------

func editMessage(content *event.MessageEventContent, target networkid.MessageID) *bridgev2.MatrixEdit {
	return &bridgev2.MatrixEdit{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
			Content: content,
			Portal:  newTestPortal(networkid.PortalID(outDMThread), ""),
		},
		EditTarget: &database.Message{ID: target},
	}
}

func TestHandleMatrixEditSendsFormattedBody(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	err := c.HandleMatrixEdit(context.Background(), editMessage(&event.MessageEventContent{
		MsgType: event.MsgText, Body: "fixed", Format: event.FormatHTML, FormattedBody: "<b>fixed</b>",
	}, "1726000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.sentEdits) != 1 {
		t.Fatalf("edits %v", api.sentEdits)
	}
	e := api.sentEdits[0]
	if e.ThreadID != outDMThread || e.MessageID != "1726000000001" || e.NewHTML != "<b>fixed</b>" || e.FromUserID != testSelfUserID {
		t.Errorf("edit %+v", e)
	}
}

// BUG: an edit's plain body goes to Teams as RichText/Html unescaped and
// unwrapped, unlike a new message (plaintextToTeamsHTML), so "<" and "&"
// are taken as markup and newlines collapse; an HTML edit also skips
// matrixHTMLToTeamsHTML and mention conversion.  This documents the
// current behaviour; see the report.
func TestHandleMatrixEditPlainBodyIsNotEscaped(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	err := c.HandleMatrixEdit(context.Background(), editMessage(&event.MessageEventContent{
		MsgType: event.MsgText, Body: "x < y & z",
	}, "1726000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if got := api.sentEdits[0].NewHTML; got != "x < y & z" {
		t.Fatalf("edit body %q: the escaping bug is fixed, update this test", got)
	}
}

func TestHandleMatrixEditMissingTargetID(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	err := c.HandleMatrixEdit(context.Background(), editMessage(&event.MessageEventContent{MsgType: event.MsgText, Body: "x"}, ""))
	if err == nil || len(api.sentEdits) != 0 {
		t.Fatalf("err %v, edits %v", err, api.sentEdits)
	}
}

func TestHandleMatrixMessageRemoveSendsDelete(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	msg := &bridgev2.MatrixMessageRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: newTestPortal(networkid.PortalID(outChannelThread), ""),
		},
		TargetMessage: &database.Message{ID: "1726000000001"},
	}
	if err := c.HandleMatrixMessageRemove(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	want := sentDelete{ThreadID: outChannelThread, MessageID: "1726000000001", FromUserID: testSelfUserID}
	if len(api.sentDeletes) != 1 || api.sentDeletes[0] != want {
		t.Fatalf("deletes %v", api.sentDeletes)
	}

	msg.TargetMessage = &database.Message{}
	if err := c.HandleMatrixMessageRemove(context.Background(), msg); err == nil {
		t.Fatal("empty target ID accepted")
	}
}

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func reaction(key string) *bridgev2.MatrixReaction {
	return &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
			Content: &event.ReactionEventContent{RelatesTo: event.RelatesTo{Key: key}},
			Portal:  newTestPortal(networkid.PortalID(outGroupThread), ""),
		},
		TargetMessage: &database.Message{ID: "msg/1726000000001"},
	}
}

func TestPreHandleMatrixReaction(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	resp, err := c.PreHandleMatrixReaction(context.Background(), reaction("\U0001f44d"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.EmojiID != "like" || resp.Emoji != "\U0001f44d" || string(resp.SenderID) != testSelfUserID {
		t.Errorf("resp %+v", resp)
	}

	if _, err := c.PreHandleMatrixReaction(context.Background(), reaction("not an emoji")); !isStatus(err, errUnsupportedReactionEmoji) {
		t.Errorf("unsupported key: err %v", err)
	}

	c.Meta.TeamsUserID = ""
	if _, err := c.PreHandleMatrixReaction(context.Background(), reaction("\U0001f44d")); err == nil {
		t.Error("no error without a Teams user ID")
	}

	c.Meta.TeamsUserID = testSelfUserID
	c.loggedIn.Store(false)
	c.Meta.SkypeToken = ""
	if _, err := c.PreHandleMatrixReaction(context.Background(), reaction("\U0001f44d")); !errors.Is(err, bridgev2.ErrNotLoggedIn) {
		t.Errorf("logged out: err %v", err)
	}
}

func TestHandleMatrixReactionWithoutPreHandleMapsEmoji(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	r, err := c.HandleMatrixReaction(context.Background(), reaction("❤️"))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.sentReactions) != 1 {
		t.Fatalf("reactions %v", api.sentReactions)
	}
	got := api.sentReactions[0]
	// The stored "msg/" prefix is not part of the Teams message ID.
	if got.ThreadID != outGroupThread || got.MessageID != "1726000000001" || got.EmotionKey != string(r.EmojiID) || got.EmotionKey != "heart" {
		t.Errorf("reaction %+v, db %+v", got, r)
	}

	msg := reaction("\U0001f44d")
	msg.TargetMessage = nil
	if _, err := c.HandleMatrixReaction(context.Background(), msg); !isStatus(err, bridgev2.ErrTargetMessageNotFound) {
		t.Errorf("missing target: err %v", err)
	}
	if _, err := c.HandleMatrixReaction(context.Background(), reaction("not an emoji")); !isStatus(err, errUnsupportedReactionEmoji) {
		t.Errorf("unsupported: err %v", err)
	}
}

func TestHandleMatrixReactionRemoveByEmoji(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	msg := &bridgev2.MatrixReactionRemove{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{
			Portal: newTestPortal(networkid.PortalID(outGroupThread), ""),
		},
		TargetReaction: &database.Reaction{MessageID: "msg/1726000000001", Emoji: "\U0001f44d"},
	}
	if err := c.HandleMatrixReactionRemove(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	want := removedReaction{ThreadID: outGroupThread, MessageID: "1726000000001", EmotionKey: "like"}
	if len(api.removedReactions) != 1 || api.removedReactions[0] != want {
		t.Fatalf("removed %v", api.removedReactions)
	}

	// An emoji Teams has no key for was never sent, so there is nothing to remove.
	msg.TargetReaction = &database.Reaction{MessageID: "1726000000001", Emoji: "not an emoji"}
	if err := c.HandleMatrixReactionRemove(context.Background(), msg); err != nil || len(api.removedReactions) != 1 {
		t.Fatalf("err %v, removed %v", err, api.removedReactions)
	}
}

// ---------------------------------------------------------------------------
// Read receipts, membership, avatar
// ---------------------------------------------------------------------------

func TestHandleMatrixReadReceiptOncePerUnreadRun(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	receipt := &bridgev2.MatrixReadReceipt{Portal: newTestPortal(networkid.PortalID(outDMThread), "")}

	// Nothing unread: no call.
	if err := c.HandleMatrixReadReceipt(context.Background(), receipt); err != nil || len(api.setHorizons) != 0 {
		t.Fatalf("err %v, horizons %v", err, api.setHorizons)
	}
	c.markUnread(outDMThread)
	before := time.Now().UnixMilli()
	for i := 0; i < 2; i++ {
		if err := c.HandleMatrixReadReceipt(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.setHorizons) != 1 || api.setHorizons[0].ThreadID != outDMThread {
		t.Fatalf("horizons %v", api.setHorizons)
	}
	// "<ms>;<ms>;0"
	parts := strings.Split(api.setHorizons[0].Horizon, ";")
	ms, _ := strconv.ParseInt(parts[0], 10, 64)
	if len(parts) != 3 || parts[0] != parts[1] || parts[2] != "0" || ms < before {
		t.Errorf("horizon %q", api.setHorizons[0].Horizon)
	}
}

func TestHandleMatrixMembershipIgnoresNonGhostsAndOtherTypes(t *testing.T) {
	api := &mockTeamsAPI{}
	c := newTestClient(api, &capturingEventSink{})
	ghost := &bridgev2.Ghost{Ghost: &database.Ghost{ID: "8:orgid:00000002-0000-0000-0000-000000000002"}}
	// Another bridge user's login: a real Matrix user, not a Teams ghost.
	user := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "other-login"}}
	portal := newTestPortal(networkid.PortalID(outGroupThread), "")
	for _, tc := range []struct {
		typ    bridgev2.MembershipChangeType
		target bridgev2.GhostOrUserLogin
	}{
		{bridgev2.Join, ghost},
		{bridgev2.Leave, ghost},
		{bridgev2.Invite, nil},
		{bridgev2.Invite, user},
	} {
		msg := &bridgev2.MatrixMembershipChange{
			MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
				MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{Portal: portal},
			},
			Target: tc.target,
			Type:   tc.typ,
		}
		if _, err := c.HandleMatrixMembership(context.Background(), msg); err != nil {
			t.Errorf("%v: %v", tc.typ, err)
		}
	}
	if len(api.addedMembers)+len(api.removedMembers) != 0 {
		t.Fatalf("added %v removed %v", api.addedMembers, api.removedMembers)
	}
}

func TestHandleMatrixRoomAvatarNeedsGraphToken(t *testing.T) {
	c := newTestClient(&mockTeamsAPI{}, &capturingEventSink{})
	msg := &bridgev2.MatrixRoomAvatar{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomAvatarEventContent]{
			Content: &event.RoomAvatarEventContent{URL: "mxc://example.org/abc"},
			Portal:  newTestPortal(networkid.PortalID(outGroupThread), ""),
		},
	}
	ok, err := c.HandleMatrixRoomAvatar(context.Background(), msg)
	if ok || err == nil || !strings.Contains(err.Error(), "graph token required") {
		t.Fatalf("ok %v err %v", ok, err)
	}

	// With a Graph token, an avatar without a URL is refused before any request.
	c.Meta.GraphAccessToken = "graph-token"
	c.Meta.GraphExpiresAt = time.Now().Add(time.Hour).Unix()
	msg.Content.URL = ""
	if ok, err := c.HandleMatrixRoomAvatar(context.Background(), msg); ok || err == nil {
		t.Fatalf("ok %v err %v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// On the wire: the real Teams client under HandleMatrixMessage
// ---------------------------------------------------------------------------

// wireChatService stands in for the enterprise chat service: GET messages
// returns page, POST messages records the request body.
type wireChatService struct {
	mu    sync.Mutex
	page  string
	posts []map[string]any
}

func (s *wireChatService) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonRoute(http.StatusOK, s.page)(w, r)
	case http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.posts = append(s.posts, body)
		s.mu.Unlock()
		// The chat service answers a send with the arrival time.
		jsonRoute(http.StatusCreated, `{"OriginalArrivalTime":1726000000999}`)(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// newWireClient is a client on the real consumer client, with its chat
// service requests for threadID answered by svc.
func newWireClient(t *testing.T, svc *wireChatService, threadID string) *TeamsClient {
	t.Helper()
	c := newSchedTestClient(t, nil, &capturingEventSink{}, map[string]string{threadID: threadID})
	c.api = nil // use the real consumer client
	c.Meta.RegionChatServiceURL = "https://amer.ng.msg.teams.microsoft.com"
	path := "amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/" + threadID + "/messages"
	c.consumerHTTP = &http.Client{Transport: &routeTransport{routes: map[string]http.HandlerFunc{path: svc.handle}}}
	return c
}

// A page from the enterprise chat service's messages endpoint, trimmed to
// the fields the client reads, holding the message being replied to.
const wireQuotedPage = `{
  "messages": [
    {
      "id": "1726000000001",
      "sequenceId": 41,
      "clientmessageid": "8123456789012345678",
      "version": "1726000000001",
      "conversationid": "` + outGroupThread + `",
      "conversationLink": "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/` + outGroupThread + `",
      "type": "Message",
      "messagetype": "RichText/Html",
      "contenttype": "text",
      "content": "<p>Lunch at <b>noon</b>?</p>",
      "from": "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/contacts/8:orgid:00000002-0000-0000-0000-000000000002",
      "imdisplayname": "Ann Example",
      "fromDisplayNameInToken": "Ann Example",
      "composetime": "2026-09-01T12:00:00.000Z",
      "originalarrivaltime": "2026-09-01T12:00:00.000Z",
      "properties": {}
    }
  ],
  "_metadata": {"lastCompleteSegmentStartTime": 1726000000000, "lastCompleteSegmentEndTime": 1726000000001}
}`

func TestHandleMatrixMessageChatReplyOnTheWire(t *testing.T) {
	svc := &wireChatService{page: wireQuotedPage}
	c := newWireClient(t, svc, outGroupThread)
	msg := textMessage(outGroupThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "Sure"})
	msg.ReplyTo = &database.Message{ID: "1726000000001"}
	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(svc.posts) != 1 {
		t.Fatalf("posts %v", svc.posts)
	}
	post := svc.posts[0]
	wantContent := `<blockquote itemtype="http://schema.skype.com/Reply" itemid="1726000000001"><strong>Ann Example</strong><br>Lunch at noon?</blockquote><p>Sure</p>`
	if post["content"] != wantContent {
		t.Errorf("content:\n got %v\nwant %s", post["content"], wantContent)
	}
	props, _ := post["properties"].(map[string]any)
	if props["replyChainMessageId"] != "1726000000001" {
		t.Errorf("properties %v", post["properties"])
	}
	if post["messagetype"] != "RichText/Html" || post["from"] != testSelfUserID {
		t.Errorf("post %v", post)
	}
}

func TestHandleMatrixMessageChannelReplyOnTheWire(t *testing.T) {
	svc := &wireChatService{page: `{"messages":[]}`}
	c := newWireClient(t, svc, outChannelThread)
	msg := textMessage(outChannelThread, &event.MessageEventContent{MsgType: event.MsgText, Body: "on it"})
	msg.ReplyTo = &database.Message{ID: "1726000000002", ThreadRoot: "1726000000001"}
	if _, err := c.HandleMatrixMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(svc.posts) != 1 {
		t.Fatalf("posts %v", svc.posts)
	}
	post := svc.posts[0]
	if post["content"] != "<p>on it</p>" {
		t.Errorf("content %v", post["content"])
	}
	props, _ := post["properties"].(map[string]any)
	if props["replyChainMessageId"] != "1726000000001" {
		t.Errorf("properties %v", post["properties"])
	}
}
