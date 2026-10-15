package presence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
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

	c := &Client{HTTP: srv.Client(), Token: "tok", BaseURL: srv.URL + "/"}
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

func TestSetAvailabilityAndNote(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]string
	}
	var calls []call
	var lastRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lastRaw = string(raw)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, call{r.Method, r.URL.Path, body})
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Token: "tok", BaseURL: srv.URL + "/"}
	ctx := context.Background()
	until := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.SetAvailability(ctx, "DoNotDisturb", until))
	must(c.SetAvailability(ctx, "Offline", time.Time{}))
	must(c.SetAvailability(ctx, "", time.Time{}))
	must(c.PublishNote(ctx, "Office hours 2-4 <McAdams>", until, true))
	must(c.PublishNote(ctx, "", time.Time{}, false))
	must(c.SetWorkLocation(ctx, LocationOffice, until))
	want := []call{
		{"PUT", "/v1/me/forceavailability/", map[string]string{"availability": "DoNotDisturb", "desiredExpirationTime": "2026-10-14T23:00:00Z"}},
		{"PUT", "/v1/me/forceavailability/", map[string]string{"availability": "Offline", "activity": "OffWork"}},
		{"PUT", "/v1/me/forceavailability/", nil},
		{"PUT", "/v1/me/publishnote", map[string]string{"message": "Office hours 2-4 &lt;McAdams&gt;<pinnednote></pinnednote>", "expiry": "2026-10-14T23:00:00Z"}},
		{"PUT", "/v1/me/publishnote", map[string]string{"message": ""}},
		{"PUT", "/v1/me/workLocation/", nil},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %+v", len(calls), len(want), calls)
	}
	for i := range want {
		sameBody := want[i].path == "/v1/me/workLocation/" || reflect.DeepEqual(calls[i].body, want[i].body)
		if calls[i].method != want[i].method || calls[i].path != want[i].path || !sameBody {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
	// The work location's number does not decode as a string map.
	if lastRaw != `{"expirationTime":"2026-10-14T23:00:00Z","location":1}` {
		t.Errorf("work location body = %s", lastRaw)
	}
}

func TestWorkLocationWords(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var p Presence
	if err := json.Unmarshal([]byte(`{"workLocation":{"location":1,"expiry":"2026-10-16T04:00:00Z"}}`), &p); err != nil {
		t.Fatal(err)
	}
	if got := p.WorkLocation.Words(now); got != "In the office" {
		t.Errorf("office: %q", got)
	}
	if got := p.WorkLocation.Words(now.Add(24 * time.Hour)); got != "" {
		t.Errorf("expired: %q", got)
	}
	if got := (WorkLocation{Location: "2"}).Words(now); got != "Remote" {
		t.Errorf("remote: %q", got)
	}
	// Names and odd values must not fail the whole presence response.
	for raw, want := range map[string]string{
		`{"workLocation":{"location":"Office"}}`: "In the office",
		`{"workLocation":{"location":null}}`:     "",
		`{"workLocation":{"location":{"x":1}}}`:  "",
		`{"availability":"Busy"}`:                "",
	} {
		var p Presence
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Errorf("%s: %v", raw, err)
		} else if got := p.WorkLocation.Words(now); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
