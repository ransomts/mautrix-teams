package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

func (c *TeamsClient) portalKey(threadID string) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       networkid.PortalID(strings.TrimSpace(threadID)),
		Receiver: c.Login.ID,
	}
}

func teamsUserIDToNetworkUserID(teamsUserID string) networkid.UserID {
	return networkid.UserID(strings.TrimSpace(teamsUserID))
}

// isLikelyThreadID reports whether value is a conversation rather than a
// user: thread IDs start with "19:" (chats, channels "@thread.tacv2",
// meetings), user IDs with "8:".  Teams sends channel system messages
// (member added, topic changed) from the channel itself, and a ghost must
// never be minted for those.
func isLikelyThreadID(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "19:") ||
		strings.Contains(normalized, "@thread.v2") ||
		strings.Contains(normalized, "@thread.tacv2") ||
		strings.Contains(normalized, "@thread.skype") ||
		strings.Contains(normalized, "@unq.gbl.spaces")
}

// isSystemMessageType reports whether a Teams message type is a
// conversation-control message with no user content: ThreadActivity/*
// (membership, role, topic and picture changes; the ones worth reading are
// rendered as notices, see system_messages.go) and the typing/live-state
// controls.
func isSystemMessageType(messageType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(messageType))
	return strings.HasPrefix(normalized, "threadactivity/") || strings.HasPrefix(normalized, "control/")
}
