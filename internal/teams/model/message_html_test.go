package model

import "testing"

func TestNormalizeMessageBodyHTML(t *testing.T) {
	content := NormalizeMessageBody("<p>hi</p><p>there<br>friend</p>")
	if content.Body != "hi\nthere\nfriend" {
		t.Fatalf("unexpected plaintext body: %q", content.Body)
	}
	if content.FormattedBody != "<p>hi</p><p>there<br>friend</p>" {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
}

func TestNormalizeMessageBodySanitizesUnsafeHTML(t *testing.T) {
	content := NormalizeMessageBody(`<p>Hello <a href="javascript:alert('x')" onclick="evil()">world</a></p><script>alert(1)</script>`)
	if content.Body != "Hello world" {
		t.Fatalf("unexpected plaintext body: %q", content.Body)
	}
	if content.FormattedBody != "<p>Hello <a>world</a></p>" {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
}

func TestNormalizeMessageBodyPlainText(t *testing.T) {
	content := NormalizeMessageBody("hey how&apos;ve u been?")
	if content.Body != "hey how've u been?" {
		t.Fatalf("unexpected plaintext body: %q", content.Body)
	}
	if content.FormattedBody != "" {
		t.Fatalf("expected empty formatted body, got %q", content.FormattedBody)
	}
}

func TestNormalizeMessageBodyUnsupportedTagFallsBackToPlain(t *testing.T) {
	content := NormalizeMessageBody("<custom>hello</custom>")
	if content.Body != "hello" {
		t.Fatalf("unexpected plaintext body: %q", content.Body)
	}
	if content.FormattedBody != "" {
		t.Fatalf("expected empty formatted body, got %q", content.FormattedBody)
	}
}

// A message that is only a Teams emoticon is an <img> with the emoji in its
// alt attribute and no text node; it must not come out empty.
func TestNormalizeMessageBodyEmoticonImage(t *testing.T) {
	raw := `<p><span title="Shrimp" type="(1f990_shrimp)" class="animated-emoticon-20-1f990_shrimp" itemscope=""><img itemscope="" itemtype="http://schema.skype.com/Emoji" itemid="1f990_shrimp" src="https://statics.teams.cdn.office.net/x/20_f.png" title="Shrimp" alt="🦐" style="width:20px; height:20px"></span></p>`
	content := NormalizeMessageBody(raw)
	if content.Body != "🦐" {
		t.Fatalf("unexpected body: %q", content.Body)
	}
	if content.FormattedBody != "<p>🦐</p>" {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
}

func TestNormalizeMessageBodyEmoticonInText(t *testing.T) {
	raw := `<div>great <span class="animated-emoticon-58-speechless" title="Speechless"><img itemtype="http://schema.skype.com/Emoji" itemid="speechless" src="https://x/100_f.png" alt="😶"></span> ok</div>`
	content := NormalizeMessageBody(raw)
	if content.Body != "great 😶 ok" {
		t.Fatalf("unexpected body: %q", content.Body)
	}
}

func TestNormalizeMessageBodyEmoticonWithoutAlt(t *testing.T) {
	raw := `<p><img itemtype="http://schema.skype.com/Emoji" itemid="1f47a_japanesegoblin" src="https://x/20_f.png" title="Goblin"></p>`
	content := NormalizeMessageBody(raw)
	if content.Body != ":goblin:" {
		t.Fatalf("unexpected body: %q", content.Body)
	}
}
