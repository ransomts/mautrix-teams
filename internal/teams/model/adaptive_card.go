package model

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

// AdaptiveCard represents a simplified Teams Adaptive Card.
type AdaptiveCard struct {
	Type    string        `json:"type"`
	Body    []CardElement `json:"body"`
	Actions []CardAction  `json:"actions"`
}

// CardElement represents a single element in an Adaptive Card body.
type CardElement struct {
	Type    string        `json:"type"`
	Text    string        `json:"text"`
	Size    string        `json:"size,omitempty"`
	Weight  string        `json:"weight,omitempty"`
	URL     string        `json:"url,omitempty"`
	AltText string        `json:"altText,omitempty"`
	Columns []CardColumn  `json:"columns,omitempty"`
	Items   []CardElement `json:"items,omitempty"`
}

// CardColumn represents a column in a ColumnSet.
type CardColumn struct {
	Type  string        `json:"type"`
	Items []CardElement `json:"items,omitempty"`
}

// CardAction represents an action button on an Adaptive Card.
type CardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// ExtractAdaptiveCards parses Adaptive Cards from a message's properties JSON.
func ExtractAdaptiveCards(properties json.RawMessage) []AdaptiveCard {
	if len(properties) == 0 {
		return nil
	}
	var payload struct {
		Cards []struct {
			Content AdaptiveCard `json:"content"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return nil
	}
	cards := make([]AdaptiveCard, 0, len(payload.Cards))
	for _, c := range payload.Cards {
		if c.Content.Type == "AdaptiveCard" || len(c.Content.Body) > 0 || len(c.Content.Actions) > 0 {
			cards = append(cards, c.Content)
		}
	}
	return cards
}

// RenderPlainText renders a simplified plain text version of the card.
func (c AdaptiveCard) RenderPlainText() string {
	var lines []string
	for _, el := range c.Body {
		if text := renderElementText(el); text != "" {
			lines = append(lines, text)
		}
	}
	for _, action := range c.Actions {
		if action.Title != "" {
			if action.URL != "" {
				lines = append(lines, fmt.Sprintf("[%s](%s)", action.Title, action.URL))
			} else {
				lines = append(lines, fmt.Sprintf("[%s]", action.Title))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// RenderHTML renders a simplified HTML version of the card.
func (c AdaptiveCard) RenderHTML() string {
	var b strings.Builder
	b.WriteString("<blockquote>")
	for _, el := range c.Body {
		renderElementHTML(&b, el)
	}
	if len(c.Actions) > 0 {
		b.WriteString("<p>")
		for i, action := range c.Actions {
			if i > 0 {
				b.WriteString(" | ")
			}
			if action.URL != "" {
				b.WriteString(fmt.Sprintf(`<a href="%s">%s</a>`,
					html.EscapeString(action.URL), html.EscapeString(action.Title)))
			} else {
				b.WriteString(fmt.Sprintf("[%s]", html.EscapeString(action.Title)))
			}
		}
		b.WriteString("</p>")
	}
	b.WriteString("</blockquote>")
	return b.String()
}

func renderElementText(el CardElement) string {
	switch el.Type {
	case "TextBlock":
		return strings.TrimSpace(el.Text)
	case "Image":
		if el.AltText != "" {
			return fmt.Sprintf("[Image: %s]", el.AltText)
		}
		return "[Image]"
	case "ColumnSet":
		var parts []string
		for _, col := range el.Columns {
			for _, item := range col.Items {
				if t := renderElementText(item); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " | ")
	case "Container":
		var parts []string
		for _, item := range el.Items {
			if t := renderElementText(item); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func renderElementHTML(b *strings.Builder, el CardElement) {
	switch el.Type {
	case "TextBlock":
		text := strings.TrimSpace(el.Text)
		if text == "" {
			return
		}
		if el.Weight == "Bolder" || el.Size == "Large" || el.Size == "ExtraLarge" {
			b.WriteString("<p><strong>")
			b.WriteString(html.EscapeString(text))
			b.WriteString("</strong></p>")
		} else {
			b.WriteString("<p>")
			b.WriteString(html.EscapeString(text))
			b.WriteString("</p>")
		}
	case "Image":
		if el.URL != "" {
			alt := el.AltText
			if alt == "" {
				alt = "Image"
			}
			b.WriteString(fmt.Sprintf(`<p><img src="%s" alt="%s"></p>`,
				html.EscapeString(el.URL), html.EscapeString(alt)))
		}
	case "ColumnSet":
		for _, col := range el.Columns {
			for _, item := range col.Items {
				renderElementHTML(b, item)
			}
		}
	case "Container":
		for _, item := range el.Items {
			renderElementHTML(b, item)
		}
	}
}
