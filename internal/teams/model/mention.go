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
		Mentions json.RawMessage `json:"mentions"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil || len(payload.Mentions) == 0 {
		return nil
	}
	raw := payload.Mentions
	// Teams sends the list as a JSON string holding a JSON array.
	if len(raw) > 0 && raw[0] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil
		}
		raw = json.RawMessage(decoded)
	}
	var mentions []struct {
		ItemID      json.RawMessage `json:"itemid"`
		ID          json.RawMessage `json:"id"`
		MRI         string          `json:"mri"`
		DisplayName string          `json:"displayName"`
	}
	if err := json.Unmarshal(raw, &mentions); err != nil || len(mentions) == 0 {
		return nil
	}
	result := make(map[string]string, len(mentions))
	for _, m := range mentions {
		mri := strings.TrimSpace(m.MRI)
		if mri == "" {
			continue
		}
		// The index is "itemid" (a number) in what Teams sends; "id" is
		// kept for older payloads.  Either can be a number or a string.
		idStr := parseMentionID(m.ItemID)
		if idStr == "" {
			idStr = parseMentionID(m.ID)
		}
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
