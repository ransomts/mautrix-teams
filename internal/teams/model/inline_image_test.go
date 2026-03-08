package model

import "testing"

func TestParseInlineImagesFromHTML(t *testing.T) {
	raw := `<p>Check this out</p><img src="https://us-api.asm.skype.com/v1/objects/0-wus-d1-abc123/views/imgo" alt="Pasted image">`
	images, ok := ParseInlineImagesFromHTML(raw)
	if !ok {
		t.Fatal("expected inline image parse success")
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].URL != "https://us-api.asm.skype.com/v1/objects/0-wus-d1-abc123/views/imgo" {
		t.Fatalf("unexpected url: %q", images[0].URL)
	}
	if images[0].Alt != "Pasted image" {
		t.Fatalf("unexpected alt: %q", images[0].Alt)
	}
}

func TestParseInlineImagesSkipsGiphy(t *testing.T) {
	raw := `<readonly itemtype="http://schema.skype.com/Giphy"><img src="https://media4.giphy.com/media/test/giphy.gif" itemtype="http://schema.skype.com/Giphy"></readonly>`
	images, ok := ParseInlineImagesFromHTML(raw)
	if ok || len(images) > 0 {
		t.Fatalf("expected no inline images for giphy content, got %d", len(images))
	}
}

func TestParseInlineImagesMultiple(t *testing.T) {
	raw := `<img src="https://us-api.asm.skype.com/v1/objects/abc/views/imgo"><img src="https://us-api.asm.skype.com/v1/objects/def/views/imgo">`
	images, ok := ParseInlineImagesFromHTML(raw)
	if !ok {
		t.Fatal("expected parse success")
	}
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
}

func TestParseInlineImagesDeduplicate(t *testing.T) {
	raw := `<img src="https://us-api.asm.skype.com/v1/objects/abc/views/imgo"><img src="https://us-api.asm.skype.com/v1/objects/abc/views/imgo">`
	images, ok := ParseInlineImagesFromHTML(raw)
	if !ok {
		t.Fatal("expected parse success")
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 deduplicated image, got %d", len(images))
	}
}

func TestParseInlineImagesSkipsEmoji(t *testing.T) {
	raw := `<p>Hello!<span title="Smile" type="(smile)" class="animated-emoticon-20-smile" itemscope=""><img itemscope="" itemtype="http://schema.skype.com/Emoji" itemid="smile" src="https://statics.teams.cdn.office.net/evergreen-assets/personal-expressions/v2/assets/emoticons/smile/default/20_f.png" title="Smile" alt="🙂" style="width:20px; height:20px"></span></p>`
	images, ok := ParseInlineImagesFromHTML(raw)
	if ok || len(images) > 0 {
		t.Fatalf("expected no inline images for emoji content, got %d", len(images))
	}
}

func TestParseInlineImagesNoImages(t *testing.T) {
	raw := `<p>Just some text</p>`
	images, ok := ParseInlineImagesFromHTML(raw)
	if ok || len(images) > 0 {
		t.Fatal("expected no inline images")
	}
}
