package model

import (
	"strings"

	nethtml "golang.org/x/net/html"
)

// TeamsInlineImage represents a pasted/inline image in a Teams message.
// These appear as <img> tags in the HTML body with src URLs pointing to
// the Teams AMS (Azure Media Services) endpoint.
type TeamsInlineImage struct {
	URL string
	Alt string
}

// ParseInlineImagesFromHTML extracts non-GIF <img> tags from Teams HTML.
// GIF images (identified by the Giphy itemtype) are handled separately.
func ParseInlineImagesFromHTML(raw string) ([]TeamsInlineImage, bool) {
	if !looksLikeHTML(raw) {
		return nil, false
	}
	doc, err := nethtml.Parse(strings.NewReader("<div>" + raw + "</div>"))
	if err != nil {
		return nil, false
	}

	var images []TeamsInlineImage
	seen := make(map[string]struct{})
	collectInlineImages(doc, &images, seen)
	if len(images) == 0 {
		return nil, false
	}
	return images, true
}

func collectInlineImages(node *nethtml.Node, images *[]TeamsInlineImage, seen map[string]struct{}) {
	if node == nil {
		return
	}
	if node.Type == nethtml.ElementNode {
		tag := strings.ToLower(node.Data)
		// Skip GIF containers — those are handled by ParseGIFsFromHTML.
		if tag == "readonly" && hasGiphyItemType(node) {
			return
		}
		if tag == "img" && !hasGiphyItemType(node) && !hasEmojiItemType(node) {
			src := strings.TrimSpace(getAttr(node, "src"))
			if src != "" && isSafeHTTPURL(src) {
				if _, exists := seen[src]; !exists {
					alt := strings.TrimSpace(getAttr(node, "alt"))
					*images = append(*images, TeamsInlineImage{URL: src, Alt: alt})
					seen[src] = struct{}{}
				}
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectInlineImages(child, images, seen)
	}
}
