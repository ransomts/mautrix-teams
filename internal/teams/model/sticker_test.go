package model

import "testing"

func TestParseStickerFromHTML_StickerTag(t *testing.T) {
	html := `<img itemtype="http://schema.skype.com/Sticker" src="https://stickers.example.com/v2/sticker.png" alt="Happy">`
	url, alt, ok := ParseStickerFromHTML(html)
	if !ok {
		t.Fatal("expected sticker to be detected")
	}
	if url != "https://stickers.example.com/v2/sticker.png" {
		t.Errorf("unexpected url: %s", url)
	}
	if alt != "Happy" {
		t.Errorf("unexpected alt: %s", alt)
	}
}

func TestParseStickerFromHTML_FlikTag(t *testing.T) {
	html := `<readonly itemtype="http://schema.skype.com/Flik" src="https://flik.example.com/anim.gif" alt="Dancing">`
	url, alt, ok := ParseStickerFromHTML(html)
	if !ok {
		t.Fatal("expected Flik to be detected")
	}
	if url != "https://flik.example.com/anim.gif" {
		t.Errorf("unexpected url: %s", url)
	}
	if alt != "Dancing" {
		t.Errorf("unexpected alt: %s", alt)
	}
}

func TestParseStickerFromHTML_NoSticker(t *testing.T) {
	html := `<p>Hello world</p>`
	_, _, ok := ParseStickerFromHTML(html)
	if ok {
		t.Error("expected no sticker for plain HTML")
	}
}

func TestParseStickerFromHTML_MissingSrc(t *testing.T) {
	html := `<img itemtype="http://schema.skype.com/Sticker" alt="NoSrc">`
	_, _, ok := ParseStickerFromHTML(html)
	if ok {
		t.Error("expected false when src is missing")
	}
}

func TestParseStickerFromHTML_DefaultAlt(t *testing.T) {
	html := `<img itemtype="http://schema.skype.com/Sticker" src="https://example.com/sticker.png">`
	_, alt, ok := ParseStickerFromHTML(html)
	if !ok {
		t.Fatal("expected sticker to be detected")
	}
	if alt != "Sticker" {
		t.Errorf("expected default alt 'Sticker', got '%s'", alt)
	}
}
