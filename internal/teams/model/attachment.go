package model

import (
	"encoding/json"
	"strings"
)

type TeamsAttachment struct {
	Filename    string
	DriveItemID string
	ShareURL    string
	DownloadURL string
	FileType    string
}

func ExtractFilesProperty(properties json.RawMessage) string {
	if len(properties) == 0 {
		return ""
	}
	var payload struct {
		Files json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(payload.Files))
	if raw == "" || raw == "null" {
		return ""
	}
	var encoded string
	if err := json.Unmarshal(payload.Files, &encoded); err == nil {
		return strings.TrimSpace(encoded)
	}
	if strings.HasPrefix(raw, "[") {
		return raw
	}
	return ""
}

func ParseAttachments(raw string) ([]TeamsAttachment, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "\"") {
		var decoded string
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			trimmed = strings.TrimSpace(decoded)
		}
	}
	if trimmed == "" || trimmed == "[]" {
		return nil, false
	}

	var payload []struct {
		FileName string `json:"fileName"`
		FileInfo struct {
			ItemID   string `json:"itemId"`
			ShareURL string `json:"shareUrl"`
			FileURL  string `json:"fileUrl"`
		} `json:"fileInfo"`
		FileType string `json:"fileType"`
		// Files uploaded straight into a channel's SharePoint library carry
		// no share link or drive item, only their location.
		ObjectURL string `json:"objectUrl"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, false
	}
	if len(payload) == 0 {
		return nil, false
	}

	attachments := make([]TeamsAttachment, 0, len(payload))
	for _, entry := range payload {
		filename := strings.TrimSpace(entry.FileName)
		driveItemID := strings.TrimSpace(entry.FileInfo.ItemID)
		shareURL := strings.TrimSpace(entry.FileInfo.ShareURL)
		if shareURL == "" {
			// No share link: the file's own SharePoint URL opens for
			// anyone who can see the conversation.
			shareURL = strings.TrimSpace(entry.ObjectURL)
			if shareURL == "" {
				shareURL = strings.TrimSpace(entry.FileInfo.FileURL)
			}
		}
		// Keep attachments for which we have either a URL (legacy rendering) or a drive item ID
		// (inbound media re-upload).
		if filename == "" || (shareURL == "" && driveItemID == "") {
			continue
		}
		attachments = append(attachments, TeamsAttachment{
			Filename:    filename,
			DriveItemID: driveItemID,
			ShareURL:    shareURL,
			DownloadURL: strings.TrimSpace(entry.FileInfo.FileURL),
			FileType:    strings.TrimSpace(entry.FileType),
		})
	}
	if len(attachments) == 0 {
		return nil, false
	}
	return attachments, true
}

// IsDeleted reports whether a message's properties say Teams deleted it.
// The message still lists with an empty body; nothing should be made of
// it.
func IsDeleted(properties json.RawMessage) bool {
	if len(properties) == 0 {
		return false
	}
	var payload struct {
		DeleteTime     json.RawMessage `json:"deletetime"`
		HardDeleteTime json.RawMessage `json:"hardDeleteTime"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return false
	}
	return len(payload.DeleteTime) > 0 || len(payload.HardDeleteTime) > 0
}

// ExtractEditTime returns a message's properties.edittime (Unix
// milliseconds), which Teams sets when the message is edited in place, or 0
// when it was never edited.  Teams sends it as a number; a string is
// accepted too.
func ExtractEditTime(properties json.RawMessage) int64 {
	if len(properties) == 0 {
		return 0
	}
	var payload struct {
		EditTime json.RawMessage `json:"edittime"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil || len(payload.EditTime) == 0 {
		return 0
	}
	var n json.Number
	if err := json.Unmarshal(payload.EditTime, &n); err != nil {
		var s string
		if json.Unmarshal(payload.EditTime, &s) != nil {
			return 0
		}
		n = json.Number(strings.TrimSpace(s))
	}
	v, err := n.Int64()
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// ExtractSafeLinks returns the URLs in a message's "atp" property: files
// shared by link, which Teams shows as a file card with no body.
func ExtractSafeLinks(properties json.RawMessage) []string {
	if len(properties) == 0 {
		return nil
	}
	var payload struct {
		ATP json.RawMessage `json:"atp"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil || len(payload.ATP) == 0 {
		return nil
	}
	raw := strings.TrimSpace(string(payload.ATP))
	if strings.HasPrefix(raw, "\"") {
		var decoded string
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return nil
		}
		raw = decoded
	}
	var entries []struct {
		URL string `json:"URL"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	var urls []string
	for _, entry := range entries {
		if u := strings.TrimSpace(entry.URL); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// ExtractImportance returns a message's importance: "high" (Important),
// "urgent" (Urgent) or "" (standard).
func ExtractImportance(properties json.RawMessage) string {
	if len(properties) == 0 {
		return ""
	}
	var payload struct {
		Importance string `json:"importance"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return ""
	}
	switch importance := strings.ToLower(strings.TrimSpace(payload.Importance)); importance {
	case "high", "urgent":
		return importance
	default:
		return ""
	}
}

// ExtractSubject returns a channel post's title (the "subject" property),
// or "".
func ExtractSubject(properties json.RawMessage) string {
	if len(properties) == 0 {
		return ""
	}
	var payload struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Subject)
}
