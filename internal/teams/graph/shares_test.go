package graph

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	teamsclient "go.mau.fi/mautrix-teams/internal/teams/client"
)

func TestShareID(t *testing.T) {
	// The example from the Graph "Accessing shared DriveItems" docs.
	got := shareID("https://onedrive.live.com/redir?resid=1231244193912!12&authKey=1201919!12921!1")
	want := "u!aHR0cHM6Ly9vbmVkcml2ZS5saXZlLmNvbS9yZWRpcj9yZXNpZD0xMjMxMjQ0MTkzOTEyITEyJmF1dGhLZXk9MTIwMTkxOSExMjkyMSEx"
	if got != want {
		t.Fatalf("shareID = %q, want %q", got, want)
	}
}

func TestDownloadSharedItemContentAndThumbnail(t *testing.T) {
	shareURL := "https://contoso-my.sharepoint.com/:i:/g/personal/someone/abc?email=me%40contoso.edu"
	base := "/v1.0/shares/" + shareID(shareURL) + "/driveItem"
	cases := []struct {
		name string
		path string
		call func(*GraphClient) (*DriveItemContent, error)
	}{
		{"content", base + "/content", func(c *GraphClient) (*DriveItemContent, error) {
			return c.DownloadSharedItemContent(context.Background(), shareURL)
		}},
		{"thumbnail", base + "/thumbnails/0/large/content", func(c *GraphClient) (*DriveItemContent, error) {
			return c.DownloadSharedItemThumbnail(context.Background(), shareURL)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpClient := &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path != tc.path {
						t.Fatalf("unexpected path: %s", r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer tok" {
						t.Fatalf("unexpected authorization header: %s", r.Header.Get("Authorization"))
					}
					h := make(http.Header)
					h.Set("Content-Type", "image/png")
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("png"))), Header: h, Request: r}, nil
				}),
			}
			client := NewClient(httpClient)
			client.AccessToken = "tok"
			client.Executor = &teamsclient.TeamsRequestExecutor{HTTP: httpClient, MaxRetries: 0}

			got, err := tc.call(client)
			if err != nil {
				t.Fatalf("download failed: %v", err)
			}
			if string(got.Bytes) != "png" || got.ContentType != "image/png" {
				t.Fatalf("unexpected result: %#v", got)
			}
		})
	}
}

func TestDownloadSharedItemContentError(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(bytes.NewReader([]byte(`{"error":{"code":"accessDenied"}}`))), Header: make(http.Header), Request: r}, nil
		}),
	}
	client := NewClient(httpClient)
	client.AccessToken = "tok"
	client.Executor = &teamsclient.TeamsRequestExecutor{HTTP: httpClient, MaxRetries: 0}

	_, err := client.DownloadSharedItemContent(context.Background(), "https://example.test/share")
	var contentErr GraphDriveItemContentError
	if !errors.As(err, &contentErr) || contentErr.Status != http.StatusForbidden {
		t.Fatalf("expected a 403 GraphDriveItemContentError, got %v", err)
	}
}
