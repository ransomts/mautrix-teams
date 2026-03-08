package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUserByEmail(t *testing.T) {
	user := GraphUser{
		ID:                "test-uuid",
		DisplayName:       "Test User",
		Mail:              "test@example.com",
		UserPrincipalName: "test@example.com",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user)
	}))
	defer srv.Close()

	// Override the endpoint by patching the URL in a custom transport.
	gc := NewClient(srv.Client())
	gc.AccessToken = "test-token"

	// We can't easily override the URL, so let's use a transport that redirects.
	transport := &urlRewriteTransport{
		base:    srv.Client().Transport,
		baseURL: srv.URL,
	}
	gc.HTTP = &http.Client{Transport: transport}

	result, err := gc.GetUserByEmail(context.Background(), "test@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ID != "test-uuid" {
		t.Errorf("expected ID=test-uuid, got %s", result.ID)
	}
	if result.DisplayName != "Test User" {
		t.Errorf("expected DisplayName='Test User', got '%s'", result.DisplayName)
	}
}

func TestGetUserByEmail_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	gc := NewClient(&http.Client{Transport: &urlRewriteTransport{baseURL: srv.URL}})
	gc.AccessToken = "test-token"

	result, err := gc.GetUserByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("expected nil error for 404, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result for 404")
	}
}

func TestSearchPeople(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ConsistencyLevel") != "eventual" {
			t.Error("missing ConsistencyLevel header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"value": []map[string]interface{}{
				{
					"id":          "uuid-1",
					"displayName": "Alice",
					"scoredEmailAddresses": []map[string]string{
						{"address": "alice@example.com"},
					},
				},
				{
					"id":          "uuid-2",
					"displayName": "Bob",
				},
			},
		})
	}))
	defer srv.Close()

	gc := NewClient(&http.Client{Transport: &urlRewriteTransport{baseURL: srv.URL}})
	gc.AccessToken = "test-token"

	results, err := gc.SearchPeople(context.Background(), "test")
	if err != nil {
		t.Fatalf("SearchPeople failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].DisplayName != "Alice" {
		t.Errorf("expected Alice, got %s", results[0].DisplayName)
	}
	if results[0].Mail != "alice@example.com" {
		t.Errorf("expected alice@example.com, got %s", results[0].Mail)
	}
	if results[1].Mail != "" {
		t.Errorf("expected empty mail for Bob, got %s", results[1].Mail)
	}
}

// urlRewriteTransport rewrites all requests to point to the test server.
type urlRewriteTransport struct {
	base    http.RoundTripper
	baseURL string
}

func (t *urlRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[len("http://"):]
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
