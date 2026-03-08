package model

import (
	"encoding/json"
	"strconv"
	"strings"

	nethtml "golang.org/x/net/html"
)

// TeamsMention represents an @mention in a Teams message.
type TeamsMention struct {
	// UserID is the Teams user MRI (e.g. "8:orgid:abc-123").
	UserID string
	// DisplayName is the display text of the mention.
	DisplayName string
	// ItemID is the mention index from the HTML span (e.g. "0", "1").
	ItemID string
}

// ParseMentionsFromHTML extracts @mentions from Teams HTML content.
// Teams represents mentions with <span itemtype="http://schema.skype.com/Mention" itemid="INDEX">NAME</span>
// and includes a "mentions" property with MRI mappings.
// This function extracts the display names and item IDs from the HTML spans.
func ParseMentionsFromHTML(raw string) []TeamsMention {
	if !looksLikeHTML(raw) || !strings.Contains(raw, "schema.skype.com/Mention") {
		return nil
	}
	doc, err := nethtml.Parse(strings.NewReader("<div>" + raw + "</div>"))
	if err != nil {
		return nil
	}
	var mentions []TeamsMention
	collectMentions(doc, &mentions)
	return mentions
}

// ExtractMentionMRIs extracts MRI (user ID) mappings from message properties.
// The properties JSON may contain: {"mentions": [{"id": 0, "mri": "8:orgid:uuid", "displayName": "Name"}]}
func ExtractMentionMRIs(properties json.RawMessage) map[string]string {
	if len(properties) == 0 {
		return nil
	}
	var payload struct {
		Mentions []struct {
			ID          json.RawMessage `json:"id"`
			MRI         string          `json:"mri"`
			DisplayName string          `json:"displayName"`
		} `json:"mentions"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return nil
	}
	if len(payload.Mentions) == 0 {
		return nil
	}
	result := make(map[string]string, len(payload.Mentions))
	for _, m := range payload.Mentions {
		mri := strings.TrimSpace(m.MRI)
		if mri == "" {
			continue
		}
		// ID can be a number or a string in JSON.
		idStr := parseMentionID(m.ID)
		if idStr != "" {
			result[idStr] = mri
		}
	}
	return result
}

func parseMentionID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var num int
	if err := json.Unmarshal(raw, &num); err == nil {
		return strconv.Itoa(num)
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	return ""
}

// ResolveMentions combines HTML-parsed mentions with MRI data from properties.
func ResolveMentions(htmlMentions []TeamsMention, mriMap map[string]string) []TeamsMention {
	if len(htmlMentions) == 0 {
		return nil
	}
	resolved := make([]TeamsMention, len(htmlMentions))
	for i, m := range htmlMentions {
		resolved[i] = m
		if m.ItemID != "" && mriMap != nil {
			if mri, ok := mriMap[m.ItemID]; ok {
				resolved[i].UserID = mri
			}
		}
	}
	return resolved
}

func collectMentions(node *nethtml.Node, mentions *[]TeamsMention) {
	if node == nil {
		return
	}
	if node.Type == nethtml.ElementNode && strings.EqualFold(node.Data, "span") {
		itemType := strings.ToLower(strings.TrimSpace(getAttr(node, "itemtype")))
		if strings.Contains(itemType, "schema.skype.com/mention") {
			displayName := extractTextContent(node)
			itemID := strings.TrimSpace(getAttr(node, "itemid"))
			if displayName != "" {
				*mentions = append(*mentions, TeamsMention{
					DisplayName: displayName,
					ItemID:      itemID,
				})
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectMentions(child, mentions)
	}
}

func extractTextContent(node *nethtml.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == nethtml.TextNode {
		return node.Data
	}
	var sb strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		sb.WriteString(extractTextContent(child))
	}
	return strings.TrimSpace(sb.String())
}
