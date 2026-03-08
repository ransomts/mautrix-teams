package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

const defaultMessagesURL = "https://msgapi.teams.live.com/v1/users/ME/conversations"
const defaultSendMessagesURL = "https://teams.live.com/api/chatsvc/consumer/v1/users/ME/conversations"

var ErrMissingToken = errors.New("consumer client missing skypetoken")

type MessagesError struct {
	Status      int
	BodySnippet string
}

func (e MessagesError) Error() string {
	return fmt.Sprintf("messages request failed with status %d: %s", e.Status, e.BodySnippet)
}

type SendMessageError struct {
	Status      int
	BodySnippet string
}

func (e SendMessageError) Error() string {
	return "send message request failed"
}

type remoteMessage struct {
	ID                     string          `json:"id"`
	ClientMessageID        string          `json:"clientmessageid"`
	SequenceID             json.RawMessage `json:"sequenceId"`
	OriginalArrivalTime    string          `json:"originalarrivaltime"`
	From                   json.RawMessage `json:"from"`
	IMDisplayName          string          `json:"imdisplayname"`
	FromDisplayNameInToken string          `json:"fromDisplayNameInToken"`
	Content                json.RawMessage `json:"content"`
	Properties             json.RawMessage `json:"properties"`
	MessageType            string          `json:"messagetype"`
	SkypeEditedID          string          `json:"skypeeditedid"`
	ComposeTime            string          `json:"composetime"`
}

func (c *Client) ListMessages(ctx context.Context, conversationID string, sinceSequence string) ([]model.RemoteMessage, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	if conversationID == "" {
		return nil, errors.New("missing conversation id")
	}

	var payload struct {
		Messages []remoteMessage `json:"messages"`
	}
	baseURL := c.MessagesURL
	if baseURL == "" {
		baseURL = defaultMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messagesURL := fmt.Sprintf("%s/%s/messages", baseURL, url.PathEscape(conversationID))
	if err := c.fetchJSON(ctx, messagesURL, &payload); err != nil {
		return nil, err
	}

	result := make([]model.RemoteMessage, 0, len(payload.Messages))
	seen := make(map[string]struct{}, len(payload.Messages))
	for _, msg := range payload.Messages {
		msgID := strings.TrimSpace(msg.ID)
		if msgID != "" {
			if _, ok := seen[msgID]; ok {
				continue
			}
			seen[msgID] = struct{}{}
		}

		sequenceID, err := normalizeSequenceID(msg.SequenceID)
		if err != nil {
			return nil, err
		}
		senderID := model.NormalizeTeamsUserID(model.ExtractSenderID(msg.From))
		if senderID == "" && c.Log != nil {
			c.Log.Debug().
				Str("message_id", msg.ID).
				Msg("teams message missing sender id")
		}
		content := model.ExtractContent(msg.Content)
		// Extract mentions by combining HTML spans with MRI data from properties.
		var bodyRaw string
		if jsonErr := json.Unmarshal(msg.Content, &bodyRaw); jsonErr != nil {
			bodyRaw = content.Body
		}
		htmlMentions := model.ParseMentionsFromHTML(bodyRaw)
		mriMap := model.ExtractMentionMRIs(msg.Properties)
		mentions := model.ResolveMentions(htmlMentions, mriMap)

		result = append(result, model.RemoteMessage{
			MessageID:        msg.ID,
			ClientMessageID:  msg.ClientMessageID,
			SequenceID:       sequenceID,
			SenderID:         senderID,
			IMDisplayName:    msg.IMDisplayName,
			TokenDisplayName: msg.FromDisplayNameInToken,
			Timestamp:        model.ParseTimestamp(msg.OriginalArrivalTime),
			Body:             content.Body,
			FormattedBody:    content.FormattedBody,
			GIFs:             content.GIFs,
			InlineImages:     content.InlineImages,
			PropertiesFiles:  model.ExtractFilesProperty(msg.Properties),
			PropertiesRaw:    msg.Properties,
			Reactions:        model.ExtractReactions(msg.Properties),
			MessageType:      strings.TrimSpace(msg.MessageType),
			SkypeEditedID:    strings.TrimSpace(msg.SkypeEditedID),
			ReplyToID:        model.ExtractReplyToID(msg.Content),
			ThreadRootID:     model.ExtractThreadRootID(msg.Properties),
			Mentions:         mentions,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return model.CompareSequenceID(result[i].SequenceID, result[j].SequenceID) < 0
	})

	return result, nil
}

// ListMessagesPaginated fetches messages with pagination parameters.
// pageSize controls how many messages to fetch, startTime is an ISO timestamp to fetch messages before.
func (c *Client) ListMessagesPaginated(ctx context.Context, conversationID string, pageSize int, startTime string) ([]model.RemoteMessage, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	if conversationID == "" {
		return nil, errors.New("missing conversation id")
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	// Teams API caps pageSize at 200; paginate to fulfill larger requests.
	const maxPageSize = 200
	requested := pageSize
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	baseURL := c.MessagesURL
	if baseURL == "" {
		baseURL = defaultMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	result := make([]model.RemoteMessage, 0, requested)
	seen := make(map[string]struct{}, requested)
	cursor := startTime

	for len(result) < requested {
		messagesURL := fmt.Sprintf("%s/%s/messages?pageSize=%d", baseURL, url.PathEscape(conversationID), pageSize)
		if cursor != "" {
			messagesURL += "&startTime=" + url.QueryEscape(cursor)
		}

		var payload struct {
			Messages []remoteMessage `json:"messages"`
		}
		if err := c.fetchJSON(ctx, messagesURL, &payload); err != nil {
			if len(result) > 0 {
				break
			}
			return nil, err
		}
		if len(payload.Messages) == 0 {
			break
		}

		var oldestTS string
		pageBatch := 0
		for _, msg := range payload.Messages {
			msgID := strings.TrimSpace(msg.ID)
			if msgID != "" {
				if _, ok := seen[msgID]; ok {
					continue
				}
				seen[msgID] = struct{}{}
			}

			sequenceID, err := normalizeSequenceID(msg.SequenceID)
			if err != nil {
				return nil, err
			}
			senderID := model.NormalizeTeamsUserID(model.ExtractSenderID(msg.From))
			content := model.ExtractContent(msg.Content)
			var bodyRaw string
			if jsonErr := json.Unmarshal(msg.Content, &bodyRaw); jsonErr != nil {
				bodyRaw = content.Body
			}
			htmlMentions := model.ParseMentionsFromHTML(bodyRaw)
			mriMap := model.ExtractMentionMRIs(msg.Properties)
			mentions := model.ResolveMentions(htmlMentions, mriMap)

			ts := model.ParseTimestamp(msg.OriginalArrivalTime)
			if !ts.IsZero() {
				tsStr := ts.UTC().Format("2006-01-02T15:04:05.000Z")
				if oldestTS == "" || tsStr < oldestTS {
					oldestTS = tsStr
				}
			}

			result = append(result, model.RemoteMessage{
				MessageID:        msg.ID,
				ClientMessageID:  msg.ClientMessageID,
				SequenceID:       sequenceID,
				SenderID:         senderID,
				IMDisplayName:    msg.IMDisplayName,
				TokenDisplayName: msg.FromDisplayNameInToken,
				Timestamp:        ts,
				Body:             content.Body,
				FormattedBody:    content.FormattedBody,
				GIFs:             content.GIFs,
				InlineImages:     content.InlineImages,
				PropertiesFiles:  model.ExtractFilesProperty(msg.Properties),
				PropertiesRaw:    msg.Properties,
				Reactions:        model.ExtractReactions(msg.Properties),
				MessageType:      strings.TrimSpace(msg.MessageType),
				SkypeEditedID:    strings.TrimSpace(msg.SkypeEditedID),
				ReplyToID:        model.ExtractReplyToID(msg.Content),
				ThreadRootID:     model.ExtractThreadRootID(msg.Properties),
				Mentions:         mentions,
			})
			pageBatch++
		}

		// If we got fewer messages than requested page size, no more pages.
		if pageBatch < pageSize {
			break
		}
		// Use the oldest timestamp as cursor for the next page.
		if oldestTS == "" {
			break
		}
		cursor = oldestTS
	}

	sort.Slice(result, func(i, j int) bool {
		return model.CompareSequenceID(result[i].SequenceID, result[j].SequenceID) < 0
	})

	return result, nil
}

var sendMessageCounter uint64

func GenerateClientMessageID() string {
	now := uint64(time.Now().UTC().UnixNano())
	for {
		prev := atomic.LoadUint64(&sendMessageCounter)
		if now <= prev {
			now = prev + 1
		}
		if atomic.CompareAndSwapUint64(&sendMessageCounter, prev, now) {
			return strconv.FormatUint(now, 10)
		}
	}
}

// GetMessage fetches a single message by ID from a conversation.
// It scans recent messages in the thread and returns the one matching messageID.
func (c *Client) GetMessage(ctx context.Context, conversationID string, messageID string) (*model.RemoteMessage, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return nil, ErrMissingToken
	}
	conversationID = strings.TrimSpace(conversationID)
	messageID = strings.TrimSpace(messageID)
	if conversationID == "" || messageID == "" {
		return nil, errors.New("missing conversation or message id")
	}

	msgs, err := c.ListMessages(ctx, conversationID, "")
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if msgs[i].MessageID == messageID {
			return &msgs[i], nil
		}
	}
	return nil, fmt.Errorf("message %s not found in conversation", messageID)
}

func (c *Client) SendMessage(ctx context.Context, threadID string, text string, fromUserID string) (string, error) {
	clientMessageID := GenerateClientMessageID()
	_, err := c.SendMessageWithID(ctx, threadID, text, fromUserID, clientMessageID)
	return clientMessageID, err
}

func (c *Client) SendGIF(ctx context.Context, threadID string, gifURL string, title string, fromUserID string) (string, error) {
	clientMessageID := GenerateClientMessageID()
	_, err := c.SendGIFWithID(ctx, threadID, gifURL, title, fromUserID, clientMessageID)
	return clientMessageID, err
}

func (c *Client) SendMessageWithID(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string) (int, error) {
	return c.sendHTMLMessageWithID(ctx, threadID, formatHTMLContent(text), fromUserID, clientMessageID)
}

func (c *Client) SendMessageWithMentions(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, mentions []map[string]any) (int, error) {
	return c.sendRichTextMessageWithMentions(ctx, threadID, formatHTMLContent(text), fromUserID, clientMessageID, mentions)
}

func (c *Client) SendReplyWithID(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, replyToID string) (int, error) {
	return c.sendReplyMessage(ctx, threadID, formatHTMLContent(text), fromUserID, clientMessageID, replyToID, nil)
}

func (c *Client) SendReplyWithMentions(ctx context.Context, threadID string, text string, fromUserID string, clientMessageID string, replyToID string, mentions []map[string]any) (int, error) {
	return c.sendReplyMessage(ctx, threadID, formatHTMLContent(text), fromUserID, clientMessageID, replyToID, mentions)
}

func (c *Client) SendGIFWithID(ctx context.Context, threadID string, gifURL string, title string, fromUserID string, clientMessageID string) (int, error) {
	return c.sendHTMLMessageWithID(ctx, threadID, formatGIFContent(gifURL, title), fromUserID, clientMessageID)
}

func (c *Client) sendHTMLMessageWithID(ctx context.Context, threadID string, htmlContent string, fromUserID string, clientMessageID string) (int, error) {
	return c.sendRichTextMessageWithID(ctx, threadID, htmlContent, "", fromUserID, clientMessageID, false)
}

func (c *Client) SendAttachmentMessageWithID(ctx context.Context, threadID string, htmlContent string, filesProperty string, fromUserID string, clientMessageID string) (int, error) {
	if strings.TrimSpace(filesProperty) == "" {
		return 0, errors.New("missing files property")
	}
	return c.sendRichTextMessageWithID(ctx, threadID, htmlContent, filesProperty, fromUserID, clientMessageID, true)
}

func (c *Client) sendRichTextMessageWithID(ctx context.Context, threadID string, htmlContent string, filesProperty string, fromUserID string, clientMessageID string, allowEmptyContent bool) (int, error) {
	if c == nil || c.HTTP == nil {
		return 0, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return 0, ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("missing thread id")
	}
	if !allowEmptyContent && strings.TrimSpace(htmlContent) == "" {
		return 0, errors.New("missing message content")
	}
	if strings.TrimSpace(fromUserID) == "" {
		return 0, errors.New("missing from user id")
	}
	if clientMessageID == "" {
		return 0, errors.New("missing client message id")
	}

	if !strings.Contains(threadID, "@thread.v2") && c.Log != nil {
		c.Log.Warn().
			Str("thread_id", threadID).
			Msg("teams thread id missing @thread.v2")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messagesURL := fmt.Sprintf("%s/%s/messages", baseURL, url.PathEscape(threadID))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload := map[string]interface{}{
		"type":                "Message",
		"conversationid":      threadID,
		"content":             htmlContent,
		"messagetype":         "RichText/Html",
		"contenttype":         "Text",
		"clientmessageid":     clientMessageID,
		"composetime":         now,
		"originalarrivaltime": now,
		"from":                fromUserID,
		"fromUserId":          fromUserID,
	}
	if strings.TrimSpace(filesProperty) != "" {
		payload["properties"] = map[string]string{
			"files": filesProperty,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	c.debugRequest("teams send message request", messagesURL, req)

	executor := c.Executor
	if executor == nil {
		executor = &TeamsRequestExecutor{
			HTTP:        c.HTTP,
			Log:         zerolog.Nop(),
			MaxRetries:  4,
			BaseBackoff: 500 * time.Millisecond,
			MaxBackoff:  10 * time.Second,
		}
		c.Executor = executor
	}
	if executor.HTTP == nil {
		executor.HTTP = c.HTTP
	}
	if c.Log != nil {
		executor.Log = *c.Log
	}

	ctx = WithRequestMeta(ctx, RequestMeta{
		ThreadID:        threadID,
		ClientMessageID: clientMessageID,
	})
	resp, err := executor.Do(ctx, req, classifyTeamsSendResponse)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		return statusCode, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func (c *Client) sendRichTextMessageWithMentions(ctx context.Context, threadID string, htmlContent string, fromUserID string, clientMessageID string, mentions []map[string]any) (int, error) {
	if c == nil || c.HTTP == nil {
		return 0, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return 0, ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("missing thread id")
	}
	if strings.TrimSpace(htmlContent) == "" {
		return 0, errors.New("missing message content")
	}
	if strings.TrimSpace(fromUserID) == "" {
		return 0, errors.New("missing from user id")
	}
	if clientMessageID == "" {
		return 0, errors.New("missing client message id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messagesURL := fmt.Sprintf("%s/%s/messages", baseURL, url.PathEscape(threadID))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload := map[string]interface{}{
		"type":                "Message",
		"conversationid":      threadID,
		"content":             htmlContent,
		"messagetype":         "RichText/Html",
		"contenttype":         "Text",
		"clientmessageid":     clientMessageID,
		"composetime":         now,
		"originalarrivaltime": now,
		"from":                fromUserID,
		"fromUserId":          fromUserID,
		"properties": map[string]interface{}{
			"mentions": mentions,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	executor := c.Executor
	if executor == nil {
		executor = &TeamsRequestExecutor{
			HTTP:        c.HTTP,
			Log:         zerolog.Nop(),
			MaxRetries:  4,
			BaseBackoff: 500 * time.Millisecond,
			MaxBackoff:  10 * time.Second,
		}
		c.Executor = executor
	}
	if executor.HTTP == nil {
		executor.HTTP = c.HTTP
	}
	if c.Log != nil {
		executor.Log = *c.Log
	}

	ctx = WithRequestMeta(ctx, RequestMeta{
		ThreadID:        threadID,
		ClientMessageID: clientMessageID,
	})
	resp, err := executor.Do(ctx, req, classifyTeamsSendResponse)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		return statusCode, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func (c *Client) sendReplyMessage(ctx context.Context, threadID string, htmlContent string, fromUserID string, clientMessageID string, replyToID string, mentions []map[string]any) (int, error) {
	if c == nil || c.HTTP == nil {
		return 0, ErrMissingHTTPClient
	}
	if c.Token == "" {
		return 0, ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("missing thread id")
	}
	if strings.TrimSpace(htmlContent) == "" {
		return 0, errors.New("missing message content")
	}
	if strings.TrimSpace(fromUserID) == "" {
		return 0, errors.New("missing from user id")
	}
	if clientMessageID == "" {
		return 0, errors.New("missing client message id")
	}
	replyToID = strings.TrimSpace(replyToID)
	if replyToID == "" {
		return 0, errors.New("missing reply-to message id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messagesURL := fmt.Sprintf("%s/%s/messages", baseURL, url.PathEscape(threadID))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	props := map[string]interface{}{
		"replyChainMessageId": replyToID,
	}
	if len(mentions) > 0 {
		props["mentions"] = mentions
	}
	payload := map[string]interface{}{
		"type":                "Message",
		"conversationid":      threadID,
		"content":             htmlContent,
		"messagetype":         "RichText/Html",
		"contenttype":         "Text",
		"clientmessageid":     clientMessageID,
		"composetime":         now,
		"originalarrivaltime": now,
		"from":                fromUserID,
		"fromUserId":          fromUserID,
		"properties":          props,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, messagesURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	c.debugRequest("teams send reply request", messagesURL, req)

	executor := c.Executor
	if executor == nil {
		executor = &TeamsRequestExecutor{
			HTTP:        c.HTTP,
			Log:         zerolog.Nop(),
			MaxRetries:  4,
			BaseBackoff: 500 * time.Millisecond,
			MaxBackoff:  10 * time.Second,
		}
		c.Executor = executor
	}
	if executor.HTTP == nil {
		executor.HTTP = c.HTTP
	}
	if c.Log != nil {
		executor.Log = *c.Log
	}

	ctx = WithRequestMeta(ctx, RequestMeta{
		ThreadID:        threadID,
		ClientMessageID: clientMessageID,
	})
	resp, err := executor.Do(ctx, req, classifyTeamsSendResponse)
	if err != nil {
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		return statusCode, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func (c *Client) EditMessage(ctx context.Context, threadID string, messageID string, newHTMLContent string, fromUserID string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	messageID = strings.TrimSpace(messageID)
	if threadID == "" || messageID == "" {
		return errors.New("missing thread or message id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messageURL := fmt.Sprintf("%s/%s/messages/%s", baseURL, url.PathEscape(threadID), url.PathEscape(messageID))

	now := time.Now().UTC().Format(time.RFC3339Nano)
	payload := map[string]interface{}{
		"type":            "Message",
		"conversationid":  threadID,
		"content":         newHTMLContent,
		"messagetype":     "RichText/Html",
		"contenttype":     "Text",
		"composetime":     now,
		"skypeeditedid":   messageID,
		"from":            fromUserID,
		"fromUserId":      fromUserID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, messageURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("edit message failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, threadID string, messageID string, fromUserID string) error {
	if c == nil || c.HTTP == nil {
		return ErrMissingHTTPClient
	}
	if c.Token == "" {
		return ErrMissingToken
	}
	threadID = strings.TrimSpace(threadID)
	messageID = strings.TrimSpace(messageID)
	if threadID == "" || messageID == "" {
		return errors.New("missing thread or message id")
	}

	baseURL := c.SendMessagesURL
	if baseURL == "" {
		baseURL = defaultSendMessagesURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	messageURL := fmt.Sprintf("%s/%s/messages/%s", baseURL, url.PathEscape(threadID), url.PathEscape(messageID))

	payload := map[string]interface{}{
		"type":           "Message",
		"conversationid": threadID,
		"content":        "",
		"messagetype":    "RichText/Html",
		"contenttype":    "Text",
		"skypeeditedid":  messageID,
		"from":           fromUserID,
		"fromUserId":     fromUserID,
		"properties": map[string]string{
			"deletetime": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, messageURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("delete message failed with status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

func formatHTMLContent(text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	escaped := html.EscapeString(normalized)
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return "<p>" + escaped + "</p>"
}

func formatGIFContent(gifURL string, title string) string {
	gifURL = strings.TrimSpace(gifURL)
	label := strings.TrimSpace(title)
	if label == "" {
		label = "GIF"
	}
	fullLabel := label + " (GIF Image)"
	return `<p>&nbsp;</p><readonly title="` + html.EscapeString(fullLabel) + `" itemtype="http://schema.skype.com/Giphy" contenteditable="false" aria-label="` + html.EscapeString(fullLabel) + `"><img style="height:auto;margin-top:4px;max-width:100%;" alt="` + html.EscapeString(fullLabel) + `" height="250" width="350" src="` + html.EscapeString(gifURL) + `" itemtype="http://schema.skype.com/Giphy"></readonly><p>&nbsp;</p>`
}

func classifyTeamsSendResponse(resp *http.Response) error {
	if resp == nil {
		return errors.New("missing response")
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return RetryableError{
			Status:     resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return RetryableError{Status: resp.StatusCode}
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return SendMessageError{
		Status:      resp.StatusCode,
		BodySnippet: string(snippet),
	}
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if duration := time.Until(at); duration > 0 {
			return duration
		}
	}
	return 0
}

func normalizeSequenceID(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	var asString string
	if err := json.Unmarshal(value, &asString); err == nil {
		return asString, nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(value, &asNumber); err == nil {
		return asNumber.String(), nil
	}
	return "", errors.New("invalid sequenceId")
}

func (c *Client) fetchJSON(ctx context.Context, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("authentication", "skypetoken="+c.Token)
	req.Header.Set("Accept", "application/json")
	c.debugRequest("teams messages request", endpoint, req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusTooManyRequests {
			return RetryableError{
				Status:     resp.StatusCode,
				RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			return RetryableError{Status: resp.StatusCode}
		}
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return MessagesError{
			Status:      resp.StatusCode,
			BodySnippet: string(snippet),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *Client) debugRequest(message string, endpoint string, req *http.Request) {
	if c == nil || c.Log == nil {
		return
	}
	headers := map[string][]string{}
	for key, values := range req.Header {
		if strings.EqualFold(key, "authentication") || strings.EqualFold(key, "authorization") {
			headers[key] = []string{"REDACTED"}
		} else {
			headers[key] = values
		}
	}
	c.Log.Debug().
		Str("url", endpoint).
		Interface("headers", headers).
		Msg(message)
}
