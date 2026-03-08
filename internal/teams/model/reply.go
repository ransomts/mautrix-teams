package model

import (
	"encoding/json"
	"strings"

	nethtml "golang.org/x/net/html"
)

// ExtractReplyToID extracts the replied-to message ID from Teams HTML content.
// Teams represents replies with a <blockquote itemtype="http://schema.skype.com/Reply" itemid="MESSAGE_ID">.
func ExtractReplyToID(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(content, &plain); err == nil {
		return extractReplyIDFromHTML(plain)
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &obj); err == nil {
		return extractReplyIDFromHTML(obj.Text)
	}
	return ""
}

// ExtractThreadRootID extracts the replyChainMessageId from message properties.
// This identifies the root message of a channel thread.
func ExtractThreadRootID(properties json.RawMessage) string {
	if len(properties) == 0 {
		return ""
	}
	var payload struct {
		ReplyChainMessageID string `json:"replyChainMessageId"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ReplyChainMessageID)
}

func extractReplyIDFromHTML(raw string) string {
	if !looksLikeHTML(raw) || !strings.Contains(raw, "schema.skype.com/Reply") {
		return ""
	}
	doc, err := nethtml.Parse(strings.NewReader("<div>" + raw + "</div>"))
	if err != nil {
		return ""
	}
	return findReplyID(doc)
}

func findReplyID(node *nethtml.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == nethtml.ElementNode && strings.EqualFold(node.Data, "blockquote") {
		itemType := strings.ToLower(strings.TrimSpace(getAttr(node, "itemtype")))
		if strings.Contains(itemType, "schema.skype.com/reply") {
			if id := strings.TrimSpace(getAttr(node, "itemid")); id != "" {
				return id
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if id := findReplyID(child); id != "" {
			return id
		}
	}
	return ""
}
