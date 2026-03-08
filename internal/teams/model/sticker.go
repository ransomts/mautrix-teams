package model

import (
	"regexp"
	"strings"
)

// stickerRe matches Teams sticker/Flik image patterns in HTML message bodies.
var stickerRe = regexp.MustCompile(
	`<(?:img|readonly)[^>]+itemtype="http://schema\.skype\.com/(?:Sticker|Flik)"[^>]*>`,
)

// stickerSrcRe extracts the src URL from a sticker/readonly tag.
var stickerSrcRe = regexp.MustCompile(`src="([^"]+)"`)

// stickerAltRe extracts the alt text from a sticker/readonly tag.
var stickerAltRe = regexp.MustCompile(`alt="([^"]*)"`)

// ParseStickerFromHTML checks if an HTML body contains a Teams sticker and returns
// the image URL and alt text if found.
func ParseStickerFromHTML(html string) (url string, altText string, ok bool) {
	match := stickerRe.FindString(html)
	if match == "" {
		return "", "", false
	}

	srcMatch := stickerSrcRe.FindStringSubmatch(match)
	if len(srcMatch) < 2 || strings.TrimSpace(srcMatch[1]) == "" {
		// Try the whole HTML in case the src is on a nested img inside a readonly tag.
		srcMatch = stickerSrcRe.FindStringSubmatch(html)
		if len(srcMatch) < 2 || strings.TrimSpace(srcMatch[1]) == "" {
			return "", "", false
		}
	}

	altMatch := stickerAltRe.FindStringSubmatch(html)
	alt := "Sticker"
	if len(altMatch) >= 2 && strings.TrimSpace(altMatch[1]) != "" {
		alt = strings.TrimSpace(altMatch[1])
	}

	return strings.TrimSpace(srcMatch[1]), alt, true
}
