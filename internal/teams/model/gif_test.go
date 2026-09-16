package model

import "testing"

func TestParseGIFsFromHTMLGiphyReadonly(t *testing.T) {
	raw := `<p>&nbsp;</p><readonly title="Sam Darnold Football GIF (GIF Image)" itemtype="http://schema.skype.com/Giphy" contenteditable="false"><img alt="Sam Darnold Football GIF (GIF Image)" src="https://media4.giphy.com/media/test/giphy.gif" itemtype="http://schema.skype.com/Giphy"></readonly><p>&nbsp;</p>`
	gifs, ok := ParseGIFsFromHTML(raw)
	if !ok {
		t.Fatalf("expected gif parse success")
	}
	if len(gifs) != 1 {
		t.Fatalf("expected one gif, got %d", len(gifs))
	}
	if gifs[0].Title != "Sam Darnold Football GIF (GIF Image)" {
		t.Fatalf("unexpected gif title: %q", gifs[0].Title)
	}
	if gifs[0].URL != "https://media4.giphy.com/media/test/giphy.gif" {
		t.Fatalf("unexpected gif url: %q", gifs[0].URL)
	}
}

func TestExtractContentIncludesGIFs(t *testing.T) {
	content := ExtractContent([]byte(`"<readonly itemtype=\"http://schema.skype.com/Giphy\"><img alt=\"A GIF\" src=\"https://media4.giphy.com/media/test/giphy.gif\" itemtype=\"http://schema.skype.com/Giphy\"></readonly>"`))
	if len(content.GIFs) != 1 {
		t.Fatalf("expected one gif, got %#v", content.GIFs)
	}
	if content.GIFs[0].Title != "A GIF" {
		t.Fatalf("unexpected gif title: %q", content.GIFs[0].Title)
	}
}

// The GIF picker's newer markup is a bare <img> on a GIF CDN, without the
// Giphy itemtype (the form Teams sent in 2026).
func TestParseGIFsFromHTMLBareCDNImage(t *testing.T) {
	raw := `<p>&nbsp;</p>` + "\r\n" + `<img alt="Spongebob No GIF (GIF Image)" src="https://media3.giphy.com/media/v1.abc/giphy.gif" width="220" height="218" style="height:auto; margin-top:4px; max-width:100%">`
	gifs, ok := ParseGIFsFromHTML(raw)
	if !ok || len(gifs) != 1 {
		t.Fatalf("expected one gif, got ok=%v %#v", ok, gifs)
	}
	if gifs[0].URL != "https://media3.giphy.com/media/v1.abc/giphy.gif" {
		t.Fatalf("unexpected gif url: %q", gifs[0].URL)
	}
	if gifs[0].Title != "Spongebob No GIF (GIF Image)" {
		t.Fatalf("unexpected gif title: %q", gifs[0].Title)
	}
	// It is a GIF, not a pasted image needing the AMS download.
	if imgs, ok := ParseInlineImagesFromHTML(raw); ok || len(imgs) != 0 {
		t.Fatalf("CDN GIF must not be an inline image: %#v", imgs)
	}
}

func TestIsGIFCDNURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://media2.giphy.com/media/x/giphy.gif":                            true,
		"https://giphy.com/x.gif":                                               true,
		"https://media.tenor.com/x/tenor.gif":                                   true,
		"https://us-prod.asyncgw.teams.microsoft.com/v1/objects/0-x/views/imgo": false,
		"https://example.com/notgiphy.com/x.gif":                                false,
		"https://evilgiphy.com/x.gif":                                           false,
		"":                                                                      false,
	} {
		if got := IsGIFCDNURL(url); got != want {
			t.Errorf("IsGIFCDNURL(%q) = %v, want %v", url, got, want)
		}
	}
}
