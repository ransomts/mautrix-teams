package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGetSavedMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/v1/users/ME/conversations/19:none@thread.v2/rcmetadata" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.EscapedPath() != "/v1/users/ME/conversations/19:chan@thread.tacv2/rcmetadata" || r.URL.Query().Get("startTime") != "0" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"rcMetadataList":[
			{"rootMessageId":100,"rcMetadata":{"lu":500,"f":2,"sl2":[]}},
			{"rootMessageId":200,"rcMetadata":{"lu":700,"s":1,"sl2":[]}},
			{"rootMessageId":300,"rcMetadata":{"lu":600,"sl2":[301,302]}}]}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Token: "tok", MessagesURL: srv.URL + "/v1/users/ME/conversations"}
	got, err := c.GetSavedMessages(context.Background(), "19:chan@thread.tacv2")
	if err != nil {
		t.Fatal(err)
	}
	want := []SavedMessage{
		{MessageID: "200", RootID: "200", SavedAt: 700},
		{MessageID: "301", RootID: "300", SavedAt: 600},
		{MessageID: "302", RootID: "300", SavedAt: 600},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got, err := c.GetSavedMessages(context.Background(), "19:none@thread.v2"); err != nil || got != nil {
		t.Errorf("no metadata: %+v, %v", got, err)
	}
}

func TestSetMessageSaved(t *testing.T) {
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		bodies = append(bodies, string(body))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Token: "tok", MessagesURL: srv.URL + "/v1/users/ME/conversations"}
	ctx := context.Background()
	if err := c.SetMessageSaved(ctx, "48:notes", "", "123", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMessageSaved(ctx, "19:chan@thread.tacv2", "300", "301", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMessageSaved(ctx, "48:notes", "", "abc", true); err == nil {
		t.Error("bad message id accepted")
	}
	wantPaths := []string{"PUT /v1/users/ME/conversations/48:notes/rcmetadata/123", "PUT /v1/users/ME/conversations/19:chan@thread.tacv2/rcmetadata/300"}
	wantBodies := []string{`{"mid":123,"s":1}`, `{"mid":301,"s":0}`}
	if !reflect.DeepEqual(paths, wantPaths) || !reflect.DeepEqual(bodies, wantBodies) {
		t.Errorf("paths %v, bodies %v", paths, bodies)
	}
}
