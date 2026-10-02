package model

import (
	"encoding/json"
	"strings"
	"time"
)

// ForwardContext says where a forwarded message came from.
type ForwardContext struct {
	OriginalSender   string    // Teams MRI of the original author
	OriginalSentTime time.Time // zero if Teams did not say
	OriginalThreadID string    // the chat it was forwarded from
}

// ExtractForwardContext reads properties.originalMessageContext, which the
// Teams client sets (with forwardTemplateId) on a forwarded message.  Its
// content is the original wrapped in <blockquote
// itemtype="http://schema.skype.com/Forward">.  ok is false for a message
// that was not forwarded.
func ExtractForwardContext(properties json.RawMessage) (ForwardContext, bool) {
	if len(properties) == 0 {
		return ForwardContext{}, false
	}
	var payload struct {
		Context json.RawMessage `json:"originalMessageContext"`
	}
	if err := json.Unmarshal(properties, &payload); err != nil || len(payload.Context) == 0 {
		return ForwardContext{}, false
	}
	raw := payload.Context
	// Like other properties, it may come as a JSON string holding JSON.
	if raw[0] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return ForwardContext{}, false
		}
		raw = json.RawMessage(decoded)
	}
	var ctx struct {
		OriginalSender   string `json:"originalSender"`
		OriginalSentTime string `json:"originalSentTime"`
		OriginalThreadID string `json:"originalThreadId"`
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		return ForwardContext{}, false
	}
	out := ForwardContext{
		OriginalSender:   NormalizeTeamsUserID(ctx.OriginalSender),
		OriginalThreadID: strings.TrimSpace(ctx.OriginalThreadID),
	}
	if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(ctx.OriginalSentTime)); err == nil {
		out.OriginalSentTime = ts
	}
	if out.OriginalSender == "" && out.OriginalThreadID == "" {
		return ForwardContext{}, false
	}
	return out, true
}
