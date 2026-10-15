package client

// Saved messages ("Save this message").  Teams keeps them in the user's
// reply chain metadata of each conversation, one entry per reply chain (a
// chat message is a chain of its own; a channel post is the root of its
// replies):
//
//	GET {chat service}/v1/users/ME/conversations/{id}/rcmetadata?startTime=0&pageSize=N
//	  {"rcMetadataList":[{"rootMessageId":1792070027943,
//	                      "rcMetadata":{"lu":1792076049262,"s":1,"sl2":[1769715814178]}}]}
//
// s is 1 when the chain's root is saved; sl2 lists the saved replies.  A
// message is saved or unsaved with
//
//	PUT {chat service}/v1/users/ME/conversations/{id}/rcmetadata/{root}
//	  {"s":1,"mid":<message ID>}
//
// which saves the root when mid is the root and a reply otherwise.  The
// conversation list carries each conversation's lastRcMetadataVersion, so
// only conversations whose metadata changed need reading.  (The 48:saved
// stream the web app names does not exist for every user.)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const rcMetadataPageSize = 500

// SavedMessage is one saved message of a conversation.
type SavedMessage struct {
	MessageID string
	RootID    string
	// SavedAt is when the chain's metadata last changed (ms), the nearest
	// Teams gives to when the message was saved.
	SavedAt int64
}

type rcMetadataList struct {
	List []struct {
		RootMessageID json.Number `json:"rootMessageId"`
		RCMetadata    struct {
			LastUpdated json.Number   `json:"lu"`
			Saved       int           `json:"s"`
			SavedList   []json.Number `json:"sl2"`
		} `json:"rcMetadata"`
	} `json:"rcMetadataList"`
}

func (c *Client) rcMetadataURL(conversationID string, suffix string) string {
	baseURL := c.MessagesURL
	if baseURL == "" {
		baseURL = defaultMessagesURL
	}
	return fmt.Sprintf("%s/v1/users/ME/conversations/%s/rcmetadata%s",
		deriveBaseURL(baseURL), url.PathEscape(conversationID), suffix)
}

// GetSavedMessages returns the saved messages of a conversation, newest
// saved first.
func (c *Client) GetSavedMessages(ctx context.Context, conversationID string) ([]SavedMessage, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, fmt.Errorf("missing conversation id")
	}
	var resp rcMetadataList
	endpoint := c.rcMetadataURL(conversationID, "?startTime=0&pageSize="+strconv.Itoa(rcMetadataPageSize))
	if err := c.userPropertiesJSON(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		// A conversation with no metadata at all answers 404.
		if e, ok := err.(UserPropertiesError); ok && e.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return parseSavedMessages(resp), nil
}

func parseSavedMessages(resp rcMetadataList) []SavedMessage {
	var out []SavedMessage
	for _, entry := range resp.List {
		root := entry.RootMessageID.String()
		lu, _ := entry.RCMetadata.LastUpdated.Int64()
		if entry.RCMetadata.Saved == 1 && root != "" {
			out = append(out, SavedMessage{MessageID: root, RootID: root, SavedAt: lu})
		}
		for _, id := range entry.RCMetadata.SavedList {
			if id.String() != "" && id.String() != root {
				out = append(out, SavedMessage{MessageID: id.String(), RootID: root, SavedAt: lu})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SavedAt > out[j].SavedAt })
	return out
}

// SetMessageSaved saves or unsaves a message.  rootID is the root of its
// reply chain (a channel reply's post), or "" for a message that is its own
// root, as every chat message is.
func (c *Client) SetMessageSaved(ctx context.Context, conversationID string, rootID string, messageID string, saved bool) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	conversationID = strings.TrimSpace(conversationID)
	mid, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if conversationID == "" || err != nil {
		return fmt.Errorf("missing conversation or bad message id %q", messageID)
	}
	if strings.TrimSpace(rootID) == "" {
		rootID = messageID
	}
	state := 0
	if saved {
		state = 1
	}
	return c.userPropertiesJSON(ctx, http.MethodPut, c.rcMetadataURL(conversationID, "/"+url.PathEscape(strings.TrimSpace(rootID))),
		map[string]any{"s": state, "mid": mid}, nil)
}
