package model

import (
	"encoding/json"
	"strings"
)

// LinkPreview represents a URL preview extracted from Teams message properties.
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
	SiteName    string `json:"site_name"`
}

// ExtractLinkPreviews parses link preview data from message properties JSON.
func ExtractLinkPreviews(properties json.RawMessage) []LinkPreview {
	if len(properties) == 0 {
		return nil
	}
	var payload struct {
		Links []struct {
			OriginalURL  string `json:"originalUrl"`
			Title        string `json:"title"`
			Description  string `json:"description"`
			PreviewImage string `json:"previewimage"`
			SiteName     string `json:"siteName"`
		} `json:"links"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return nil
	}
	if len(payload.Links) == 0 {
		return nil
	}
	previews := make([]LinkPreview, 0, len(payload.Links))
	for _, l := range payload.Links {
		url := strings.TrimSpace(l.OriginalURL)
		if url == "" {
			continue
		}
		previews = append(previews, LinkPreview{
			URL:         url,
			Title:       strings.TrimSpace(l.Title),
			Description: strings.TrimSpace(l.Description),
			ImageURL:    strings.TrimSpace(l.PreviewImage),
			SiteName:    strings.TrimSpace(l.SiteName),
		})
	}
	return previews
}
