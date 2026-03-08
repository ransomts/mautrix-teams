package model

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Fuzz tests for model parsing functions that handle untrusted Teams data
// ---------------------------------------------------------------------------

// FuzzExtractAdaptiveCards fuzzes adaptive card JSON parsing.
func FuzzExtractAdaptiveCards(f *testing.F) {
	f.Add([]byte(`{"cards":[{"content":{"type":"AdaptiveCard","body":[{"type":"TextBlock","text":"hello"}]}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"cards": null}`))
	f.Add([]byte(`{"cards": "not an array"}`))
	f.Add([]byte(`{"cards": [{"content": null}]}`))
	f.Add([]byte(``))
	f.Add([]byte(`{{{`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cards := ExtractAdaptiveCards(json.RawMessage(data))
		for _, card := range cards {
			_ = card.RenderPlainText()
			_ = card.RenderHTML()
		}
	})
}

// FuzzExtractLinkPreviews fuzzes link preview JSON parsing.
func FuzzExtractLinkPreviews(f *testing.F) {
	f.Add([]byte(`{"links":[{"originalUrl":"https://example.com","title":"Test"}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"links": null}`))
	f.Add([]byte(`{"links": "not an array"}`))
	f.Add([]byte(``))
	f.Add([]byte(`{{{`))

	f.Fuzz(func(t *testing.T, data []byte) {
		previews := ExtractLinkPreviews(json.RawMessage(data))
		_ = previews
	})
}

// FuzzExtractThreadRootID fuzzes thread root ID extraction.
func FuzzExtractThreadRootID(f *testing.F) {
	f.Add([]byte(`{"replyChainMessageId":"123"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{{{`))
	f.Add([]byte(`{"replyChainMessageId": 42}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		result := ExtractThreadRootID(json.RawMessage(data))
		_ = result
	})
}

// FuzzParseAttachments fuzzes attachment JSON parsing.
// ParseAttachments takes a string (JSON), not json.RawMessage.
func FuzzParseAttachments(f *testing.F) {
	f.Add(`[{"fileName":"test.pdf","downloadUrl":"https://example.com/dl"}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(``)
	f.Add(`[{"fileName": null}]`)
	f.Add(`not json`)

	f.Fuzz(func(t *testing.T, data string) {
		attachments, _ := ParseAttachments(data)
		_ = attachments
	})
}

// FuzzParseStickerFromHTML fuzzes sticker HTML extraction.
func FuzzParseStickerFromHTML(f *testing.F) {
	f.Add(`<img itemtype="http://schema.skype.com/Sticker" src="https://sticker.test/img.png" alt="smile">`)
	f.Add("")
	f.Add("<img>")
	f.Add(`<img itemtype="http://schema.skype.com/Sticker">`)
	f.Add("no sticker here")
	f.Add(`<img src="x" itemtype="http://schema.skype.com/Sticker" alt="">`)

	f.Fuzz(func(t *testing.T, html string) {
		url, alt, ok := ParseStickerFromHTML(html)
		if ok && url == "" {
			t.Error("ok=true but url is empty")
		}
		_ = alt
	})
}

// FuzzParseInlineImagesFromHTML fuzzes inline image HTML extraction.
func FuzzParseInlineImagesFromHTML(f *testing.F) {
	f.Add(`<img itemtype="http://schema.skype.com/AMSImage" src="https://ams.test/img.png">`)
	f.Add("")
	f.Add("<img>")
	f.Add("no images")
	f.Add(`<img src="x" itemtype="http://schema.skype.com/AMSImage">`)

	f.Fuzz(func(t *testing.T, html string) {
		images, _ := ParseInlineImagesFromHTML(html)
		_ = images
	})
}

// FuzzNormalizeMessageBody fuzzes HTML sanitization / normalization.
func FuzzNormalizeMessageBody(f *testing.F) {
	f.Add("<p>hello</p>")
	f.Add("<script>alert(1)</script>")
	f.Add("")
	f.Add("<<>>")
	f.Add(`<a href="javascript:alert(1)">click</a>`)
	f.Add(string(make([]byte, 5000)))

	f.Fuzz(func(t *testing.T, html string) {
		result := NormalizeMessageBody(html)
		_ = result
	})
}

// FuzzParseGIFsFromHTML fuzzes GIF HTML extraction.
func FuzzParseGIFsFromHTML(f *testing.F) {
	f.Add(`<div itemtype="http://schema.skype.com/Giphy"><img src="https://giphy.com/test.gif" alt="test"></div>`)
	f.Add("")
	f.Add("<img>")
	f.Add("no gifs here")

	f.Fuzz(func(t *testing.T, html string) {
		gifs, _ := ParseGIFsFromHTML(html)
		_ = gifs
	})
}

// FuzzNormalizeTeamsUserID fuzzes user ID normalization.
func FuzzNormalizeTeamsUserID(f *testing.F) {
	f.Add("8:orgid:uuid-here")
	f.Add("8:live:user")
	f.Add("")
	f.Add("28:bot-id")
	f.Add("  8:orgid:uuid  ")

	f.Fuzz(func(t *testing.T, input string) {
		result := NormalizeTeamsUserID(input)
		_ = result
	})
}

// FuzzExtractReactions fuzzes reaction JSON parsing.
func FuzzExtractReactions(f *testing.F) {
	f.Add([]byte(`{"emotions":{"like":{"users":["8:orgid:alice"]}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{{{`))

	f.Fuzz(func(t *testing.T, data []byte) {
		reactions := ExtractReactions(json.RawMessage(data))
		_ = reactions
	})
}

// FuzzExtractMentionMRIs fuzzes mention MRI JSON extraction.
func FuzzExtractMentionMRIs(f *testing.F) {
	f.Add([]byte(`[{"id":0,"mri":"8:orgid:alice","displayName":"Alice"}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		result := ExtractMentionMRIs(json.RawMessage(data))
		_ = result
	})
}
