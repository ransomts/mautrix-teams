package presence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body []map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 2 || body[0]["mri"] != "8:orgid:a" {
			t.Errorf("unexpected request body %v (%v)", body, err)
		}
		_, _ = w.Write([]byte(`[
			{"mri":"8:orgid:a","presence":{"availability":"Busy","activity":"InAMeeting","deviceType":"Desktop"}},
			{"mri":"8:orgid:b","presence":{"availability":"Offline","activity":"Offline"}}]`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Token: "tok", URL: srv.URL}
	got, err := c.Get(context.Background(), []string{"8:orgid:a", "8:orgid:b"})
	if err != nil {
		t.Fatal(err)
	}
	if got["8:orgid:a"] != (Presence{Availability: "Busy", Activity: "InAMeeting"}) || got["8:orgid:b"].Availability != "Offline" {
		t.Errorf("got %+v", got)
	}

	c.Token = "wrong"
	if _, err := c.Get(context.Background(), []string{"8:orgid:a"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad token: err = %v, want ErrUnauthorized", err)
	}
}
