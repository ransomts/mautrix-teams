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
