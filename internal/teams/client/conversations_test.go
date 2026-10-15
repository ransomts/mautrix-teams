package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListConversationsSuccess(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authentication")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"conversations":[{"threadProperties":{"originalThreadId":"abc","productThreadType":"OneToOneChat"}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.ConversationsURL = server.URL

	convos, err := client.ListConversations(context.Background(), "token123")
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if gotAuth != "skypetoken=token123" {
		t.Fatalf("unexpected authentication header: %q", gotAuth)
	}
	if len(convos) != 1 {
		t.Fatalf("unexpected conversations length: %d", len(convos))
	}
}

func TestListConversationsNon2xx(t *testing.T) {
	body := strings.Repeat("a", maxErrorBodyBytes+10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.ConversationsURL = server.URL

	_, err := client.ListConversations(context.Background(), "token123")
	if err == nil {
		t.Fatalf("expected error")
	}
	var convErr ConversationsError
	if !errors.As(err, &convErr) {
		t.Fatalf("expected ConversationsError, got %T", err)
	}
	if convErr.Status != http.StatusForbidden {
		t.Fatalf("unexpected status: %d", convErr.Status)
	}
	if len(convErr.BodySnippet) != maxErrorBodyBytes {
		t.Fatalf("unexpected body snippet length: %d", len(convErr.BodySnippet))
	}
}

func TestListConversations429IsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.ConversationsURL = server.URL
	_, err := client.ListConversations(context.Background(), "token123")
	var retryable RetryableError
	if !errors.As(err, &retryable) || retryable.Status != http.StatusTooManyRequests || retryable.RetryAfter != 12*time.Second {
		t.Fatalf("expected a 429 RetryableError with Retry-After 12s, got %#v", err)
	}
}

func TestSetPinnedItems(t *testing.T) {
	var method, path, query string
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(server.Client())
	client.SendMessagesURL = server.URL + "/v1/threads"
	client.Token = "token123"
	if err := client.SetPinnedItems(context.Background(), "19:abc@thread.v2", []string{"2", "1"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/v1/threads/19:abc@thread.v2/properties" || query != "name=pinnedItems" {
		t.Errorf("request = %s %s?%s", method, path, query)
	}
	if body["pinnedItems"] != `[{"itemId":"2","itemType":"Message"},{"itemId":"1","itemType":"Message"}]` {
		t.Errorf("body = %v", body)
	}
}
