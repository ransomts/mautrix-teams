package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeviceCodeEndpointFor(t *testing.T) {
	cases := map[string]string{
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/token":  "https://login.microsoftonline.com/tenant/oauth2/v2.0/devicecode",
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/token/": "https://login.microsoftonline.com/tenant/oauth2/v2.0/devicecode",
		"http://127.0.0.1:1234": "http://127.0.0.1:1234/devicecode",
		"":                      "",
	}
	for in, want := range cases {
		if got := DeviceCodeEndpointFor(in); got != want {
			t.Fatalf("DeviceCodeEndpointFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestDeviceCode(t *testing.T) {
	var gotScope, gotClient, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath = r.URL.Path
		gotScope = r.Form.Get("scope")
		gotClient = r.Form.Get("client_id")
		_, _ = w.Write([]byte(`{"device_code":"dev","user_code":"ABCD-1234","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":7,"message":"go"}`))
	}))
	defer server.Close()

	client := NewClient(nil)
	client.HTTP = server.Client()
	client.ClientID = "client-x"
	client.TokenEndpoint = server.URL + "/oauth2/v2.0/token"

	dc, err := client.RequestDeviceCode(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("RequestDeviceCode failed: %v", err)
	}
	if gotPath != "/oauth2/v2.0/devicecode" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotScope != "a b" || gotClient != "client-x" {
		t.Fatalf("unexpected form: scope=%q client=%q", gotScope, gotClient)
	}
	if dc.UserCode != "ABCD-1234" || dc.DeviceCode != "dev" || dc.VerificationURI != "https://microsoft.com/devicelogin" {
		t.Fatalf("unexpected device code: %+v", dc)
	}
	if dc.Interval != 7*time.Second {
		t.Fatalf("unexpected interval %v", dc.Interval)
	}
	if until := time.Until(dc.ExpiresAt); until < 14*time.Minute || until > 15*time.Minute+time.Second {
		t.Fatalf("unexpected expiry in %v", until)
	}
}

func TestPollDeviceCodeRetriesPendingThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	var gotGrant, gotDevice string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotDevice = r.Form.Get("device_code")
		switch hits.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending","error_description":"AADSTS70016: waiting"}`))
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
		default:
			_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
		}
	}))
	defer server.Close()

	client := NewClient(nil)
	client.HTTP = server.Client()
	client.ClientID = "client-x"
	client.TokenEndpoint = server.URL
	client.Scopes = []string{"https://api.spaces.skype.com/Authorization.ReadWrite", "offline_access"}

	dc := &DeviceCode{DeviceCode: "dev", Interval: 10 * time.Millisecond, ExpiresAt: time.Now().Add(time.Minute)}
	start := time.Now()
	state, err := client.PollDeviceCode(context.Background(), dc)
	if err != nil {
		t.Fatalf("PollDeviceCode failed: %v", err)
	}
	if hits.Load() != 3 {
		t.Fatalf("expected 3 token requests, got %d", hits.Load())
	}
	if gotGrant != deviceCodeGrantType || gotDevice != "dev" {
		t.Fatalf("unexpected form: grant=%q device=%q", gotGrant, gotDevice)
	}
	if state.AccessToken != "at" || state.RefreshToken != "rt" || state.ExpiresAtUnix == 0 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.GraphAccessToken != "" {
		t.Fatalf("skype-scoped token must not be recorded as a graph token")
	}
	// slow_down adds 5s to the interval; the third request must have waited for it.
	if elapsed := time.Since(start); elapsed < deviceCodeSlowDownStep {
		t.Fatalf("slow_down was not honoured, finished after %v", elapsed)
	}
}

func TestPollDeviceCodeTerminalErrors(t *testing.T) {
	cases := []struct {
		body    string
		wantErr error
		code    string
	}{
		{`{"error":"expired_token"}`, ErrDeviceCodeExpired, ""},
		{`{"error":"authorization_declined","error_description":"AADSTS65004: declined\nTrace ID: x"}`, nil, "authorization_declined"},
		{`{"error":"bad_verification_code"}`, nil, "bad_verification_code"},
		{`{"error":"invalid_grant"}`, nil, "invalid_grant"},
		{`{"error":"invalid_client","error_description":"AADSTS7000218"}`, nil, "invalid_client"},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := NewClient(nil)
		client.HTTP = server.Client()
		client.ClientID = "client-x"
		client.TokenEndpoint = server.URL
		dc := &DeviceCode{DeviceCode: "dev", Interval: time.Millisecond, ExpiresAt: time.Now().Add(time.Minute)}
		_, err := client.PollDeviceCode(context.Background(), dc)
		server.Close()
		if err == nil {
			t.Fatalf("%s: expected error", tc.body)
		}
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: got %v, want %v", tc.body, err, tc.wantErr)
			}
			continue
		}
		var dcErr *DeviceCodeError
		if !errors.As(err, &dcErr) || dcErr.Code != tc.code {
			t.Fatalf("%s: got %v, want DeviceCodeError %s", tc.body, err, tc.code)
		}
		if dcErr.Code == "authorization_declined" && dcErr.Description != "AADSTS65004: declined" {
			t.Fatalf("description should keep only the first line, got %q", dcErr.Description)
		}
	}
}

func TestPollDeviceCodeHonoursContextAndLocalExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer server.Close()
	client := NewClient(nil)
	client.HTTP = server.Client()
	client.ClientID = "client-x"
	client.TokenEndpoint = server.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	dc := &DeviceCode{DeviceCode: "dev", Interval: time.Millisecond, ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := client.PollDeviceCode(ctx, dc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context error, got %v", err)
	}

	dc = &DeviceCode{DeviceCode: "dev", Interval: time.Millisecond, ExpiresAt: time.Now().Add(-time.Second)}
	if _, err := client.PollDeviceCode(context.Background(), dc); !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("expected local expiry, got %v", err)
	}
}
