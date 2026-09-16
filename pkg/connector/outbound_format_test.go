package connector

import "testing"

func TestPlaintextToTeamsHTMLEscapes(t *testing.T) {
	if got := plaintextToTeamsHTML("Hello <world>\nLine"); got != "<p>Hello &lt;world&gt;<br>Line</p>" {
		t.Fatalf("plaintext not escaped/wrapped correctly: %q", got)
	}
}

func TestMatrixHTMLToTeamsHTMLPassesThroughAndStripsReply(t *testing.T) {
	if got := matrixHTMLToTeamsHTML("<p>\nsounds good, thanks!</p>"); got != "<p>\nsounds good, thanks!</p>" {
		t.Fatalf("HTML should pass through, got %q", got)
	}
	in := `<mx-reply><blockquote>old</blockquote></mx-reply><p>reply body</p>`
	if got := matrixHTMLToTeamsHTML(in); got != "<p>reply body</p>" {
		t.Fatalf("mx-reply not stripped: %q", got)
	}
	if got := matrixHTMLToTeamsHTML("<strong>bold</strong> and <a href=\"http://x\">link</a>"); got != "<strong>bold</strong> and <a href=\"http://x\">link</a>" {
		t.Fatalf("formatting must survive: %q", got)
	}
}

func TestMatrixHTMLToTeamsHTMLConvertsUnderline(t *testing.T) {
	got := matrixHTMLToTeamsHTML(`<p>a <span class="underline">underline</span> b</p>`)
	want := `<p>a <u>underline</u> b</p>`
	if got != want {
		t.Fatalf("underline not converted:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestMatrixHTMLToTeamsHTMLPassesRichFormatting(t *testing.T) {
	cases := []string{
		`<strong>bold</strong>`,
		`<em>italic</em>`,
		`<u>already</u>`,
		`<del>strike</del>`,
		`<code>x = 1</code>`,
		`<a href="http://example.com">link</a>`,
		`<ul><li>a</li><li>b</li></ul>`,
		`<ol><li>one</li></ol>`,
		`<pre class="src src-python">print(1)</pre>`,
		`<blockquote>quote</blockquote>`,
	}
	for _, in := range cases {
		if got := matrixHTMLToTeamsHTML(in); got != in {
			t.Errorf("rich formatting should pass through: got %q, want %q", got, in)
		}
	}
}

func TestIsNonPollableSystemStream(t *testing.T) {
	nonPollable := []string{
		"19:teamsstream_drafts_c61ce4d2@thread.v2",
		"19:teamsstream_annotations_x@thread.v2",
		"19:teamsstream_notifications_x@thread.v2",
		"19:teamsstream_calllogs_x@thread.v2",
		"19:teamsstream_mentions_x@thread.v2",
		"19:teamsstream_threads_x@thread.v2",
	}
	for _, id := range nonPollable {
		if !isNonPollableSystemStream(id) {
			t.Errorf("%s should be non-pollable", id)
		}
	}
	pollable := []string{
		"19:teamsstream_notes_c61ce4d2@thread.v2", // the self-chat
		"19:abc123@thread.tacv2",                  // a channel
		"19:a_b@unq.gbl.spaces",                   // a DM
	}
	for _, id := range pollable {
		if isNonPollableSystemStream(id) {
			t.Errorf("%s should be pollable", id)
		}
	}
}
