package client

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

var updateSendGolden = flag.Bool("update-send-golden", false, "rewrite testdata/send_requests.golden.json")

// sendGoldenRecord is what one send call put on the wire and in the log.
type sendGoldenRecord struct {
	Name    string              `json:"name"`
	Status  int                 `json:"status"`
	Err     string              `json:"err,omitempty"`
	Method  string              `json:"method,omitempty"`
	URI     string              `json:"uri,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	Log     []string            `json:"log,omitempty"`
}

var sendTimestampRe = regexp.MustCompile(`"(composetime|originalarrivaltime)":"[^"]*"`)

// TestSendRequestsGolden pins the exact request (URI, headers, body) and log
// lines of every rich-text send path, plus the argument validation order, so
// the shared send helper stays byte-identical to the three functions it
// replaced.  Regenerate with -update-send-golden only for intended changes.
func TestSendRequestsGolden(t *testing.T) {
	mentions := []map[string]any{{
		"@type":       "http://schema.skype.com/Mention",
		"itemid":      "0",
		"mri":         "8:orgid:abc",
		"mentionType": "person",
		"displayName": "Someone",
	}}
	type sendFunc func(c *Client) (int, error)
	ctx := context.Background()
	cases := []struct {
		name string
		send sendFunc
	}{
		{"plain", func(c *Client) (int, error) {
			return c.SendMessageWithID(ctx, " 19:abc@thread.v2 ", "Hello <world>\nLine", "8:live:me", "111")
		}},
		{"plain_non_v2_thread", func(c *Client) (int, error) {
			return c.SendMessageWithID(ctx, "48:notes", "hi", "8:live:me", "112")
		}},
		{"gif", func(c *Client) (int, error) {
			return c.SendGIFWithID(ctx, "19:abc@thread.v2", "https://media.giphy.com/x.gif", "cat", "8:live:me", "113")
		}},
		{"attachment", func(c *Client) (int, error) {
			return c.SendAttachmentMessageWithID(ctx, "19:abc@thread.v2", "", `[{"fileName":"a.txt"}]`, "8:live:me", "114")
		}},
		{"attachment_missing_files", func(c *Client) (int, error) {
			return c.SendAttachmentMessageWithID(ctx, "19:abc@thread.v2", "x", " ", "8:live:me", "115")
		}},
		{"formatted_plain", func(c *Client) (int, error) {
			return c.SendFormattedMessage(ctx, "19:abc@thread.v2", "<p>hi</p>", "8:live:me", "116", "", nil)
		}},
		{"mentions", func(c *Client) (int, error) {
			return c.SendMessageWithMentions(ctx, "19:abc@thread.v2", "hi Someone", "8:live:me", "117", mentions)
		}},
		{"mentions_nil", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithMentions(ctx, "48:notes", "<p>x</p>", "8:live:me", "118", nil)
		}},
		{"formatted_mentions", func(c *Client) (int, error) {
			return c.SendFormattedMessage(ctx, "19:abc@thread.v2", "<p>hi</p>", "8:live:me", "119", "", mentions)
		}},
		{"reply", func(c *Client) (int, error) {
			return c.SendReplyWithID(ctx, "19:abc@thread.v2", "re", "8:live:me", "120", " 1700000000000 ")
		}},
		{"reply_mentions", func(c *Client) (int, error) {
			return c.SendReplyWithMentions(ctx, "48:notes", "re", "8:live:me", "121", "1700000000000", mentions)
		}},
		{"formatted_reply", func(c *Client) (int, error) {
			return c.SendFormattedMessage(ctx, "19:abc@thread.v2", "<p>re</p>", "8:live:me", "122", "1700000000000", mentions)
		}},
		{"reply_missing_reply_to", func(c *Client) (int, error) {
			return c.sendReplyMessage(ctx, "19:abc@thread.v2", "x", "8:live:me", "123", " ", nil)
		}},
		{"reply_all_missing", func(c *Client) (int, error) {
			return c.sendReplyMessage(ctx, " ", " ", " ", "", " ", nil)
		}},
		{"reply_missing_content_and_reply_to", func(c *Client) (int, error) {
			return c.sendReplyMessage(ctx, "19:abc@thread.v2", " ", "8:live:me", "124", "", nil)
		}},
		{"reply_missing_client_id_and_reply_to", func(c *Client) (int, error) {
			return c.sendReplyMessage(ctx, "19:abc@thread.v2", "x", "8:live:me", "", "", nil)
		}},
		{"plain_missing_content", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithID(ctx, "19:abc@thread.v2", " ", "", "8:live:me", "125", false)
		}},
		{"plain_missing_from", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithID(ctx, "19:abc@thread.v2", "x", "", " ", "126", false)
		}},
		{"plain_missing_client_id", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithID(ctx, "19:abc@thread.v2", "x", "", "8:live:me", "", false)
		}},
		{"mentions_missing_content", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithMentions(ctx, "19:abc@thread.v2", "", "8:live:me", "127", mentions)
		}},
		{"mentions_missing_thread", func(c *Client) (int, error) {
			return c.sendRichTextMessageWithMentions(ctx, "", "", "", "", mentions)
		}},
		{"missing_token", func(c *Client) (int, error) {
			c.Token = ""
			return c.SendReplyWithID(ctx, "19:abc@thread.v2", "x", "8:live:me", "128", "1")
		}},
		{"server_400", func(c *Client) (int, error) {
			c.SendMessagesURL += "/fail400"
			return c.SendMessageWithID(ctx, "19:abc@thread.v2", "x", "8:live:me", "129")
		}},
	}

	var records []sendGoldenRecord
	for _, tc := range cases {
		var got *sendGoldenRecord
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			headers := map[string][]string{}
			for k, v := range r.Header {
				// Content-Length varies with the timestamps' precision.
				if k != "Content-Length" {
					headers[k] = v
				}
			}
			got = &sendGoldenRecord{
				Method:  r.Method,
				URI:     r.RequestURI,
				Headers: headers,
				Body:    sendTimestampRe.ReplaceAllString(string(body), `"$1":"<now>"`),
			}
			if strings.Contains(r.URL.Path, "/fail400/") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"OriginalArrivalTime":1}`))
		}))
		var logBuf bytes.Buffer
		logger := zerolog.New(&logBuf).Level(zerolog.DebugLevel)
		c := NewClient(server.Client())
		c.SendMessagesURL = server.URL + "/conversations/"
		c.Token = "token123"
		c.Log = &logger

		status, err := tc.send(c)
		server.Close()

		rec := sendGoldenRecord{Name: tc.name, Status: status}
		if got != nil {
			rec = *got
			rec.Name = tc.name
			rec.Status = status
		}
		if err != nil {
			rec.Err = err.Error()
		}
		for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
			if line != "" {
				rec.Log = append(rec.Log, strings.ReplaceAll(line, server.URL, "<server>"))
			}
		}
		rec.Err = strings.ReplaceAll(rec.Err, server.URL, "<server>")
		records = append(records, rec)
	}

	gotJSON, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON = append(gotJSON, '\n')
	path := filepath.Join("testdata", "send_requests.golden.json")
	if *updateSendGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, gotJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-send-golden to create): %v", err)
	}
	if !bytes.Equal(want, gotJSON) {
		t.Fatalf("send requests differ from %s\n--- got ---\n%s", path, gotJSON)
	}
}
