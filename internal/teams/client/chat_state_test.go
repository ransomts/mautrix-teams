package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeProperties serves /v1/users/ME/properties the way the chat service
// does: favorites is a string holding JSON, among unrelated properties.
type fakeProperties struct {
	mu        sync.Mutex
	favorites string
	puts      []string // favorites values written
	status    int      // non-zero: answer every request with this status
}

func (f *fakeProperties) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/v1/users/ME/properties" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("authentication"); got != "skypetoken=tok" {
			t.Errorf("authentication header %q", got)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"errorCode":201}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"locale":    "en-us",
				"favorites": f.favorites,
			})
		case http.MethodPut:
			if got := r.URL.Query().Get("name"); got != "favorites" {
				t.Errorf("PUT name=%q", got)
			}
			var body map[string]string
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("PUT body %s: %v", raw, err)
			}
			f.favorites = body["favorites"]
			f.puts = append(f.puts, f.favorites)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}
}

func newPropertiesClient(t *testing.T, f *fakeProperties) *Client {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	c := NewClient(server.Client())
	c.MessagesURL = server.URL + "/v1/users/ME/conversations"
	c.Token = "tok"
	return c
}

func decodeFavorites(t *testing.T, s string) map[string]int {
	t.Helper()
	out := map[string]int{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("favorites %q: %v", s, err)
	}
	return out
}

func TestGetPinnedChats(t *testing.T) {
	f := &fakeProperties{favorites: `{"48:notes":1,"19:a_b@unq.gbl.spaces":2}`}
	c := newPropertiesClient(t, f)
	pins, err := c.GetPinnedChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins["48:notes"] != 1 || pins["19:a_b@unq.gbl.spaces"] != 2 {
		t.Fatalf("pins %v", pins)
	}
}

func TestGetPinnedChatsNoneSet(t *testing.T) {
	for _, fav := range []string{"", "{}"} {
		c := newPropertiesClient(t, &fakeProperties{favorites: fav})
		pins, err := c.GetPinnedChats(context.Background())
		if err != nil || len(pins) != 0 {
			t.Errorf("favorites %q: pins %v, err %v", fav, pins, err)
		}
	}
}

func TestParseFavoritesForms(t *testing.T) {
	cases := map[string]int{
		``:                    0,
		`null`:                0,
		`"{\"48:notes\":1}"`:  1,
		`{"48:notes":1}`:      1, // object form, should the service send it
		`"{\"a\":1,\"b\":2}"`: 2,
	}
	for raw, want := range cases {
		got, err := parseFavorites(json.RawMessage(raw))
		if err != nil || len(got) != want {
			t.Errorf("%s: %v, %v", raw, got, err)
		}
	}
	if _, err := parseFavorites(json.RawMessage(`"not json"`)); err == nil {
		t.Error("garbage favorites decoded")
	}
}

func TestSetChatPinnedAppendsAndKeepsOthers(t *testing.T) {
	f := &fakeProperties{favorites: `{"48:notes":1,"19:x@thread.v2":3}`}
	c := newPropertiesClient(t, f)
	if err := c.SetChatPinned(context.Background(), "19:new@thread.v2", true); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 {
		t.Fatalf("%d writes", len(f.puts))
	}
	got := decodeFavorites(t, f.puts[0])
	if len(got) != 3 || got["48:notes"] != 1 || got["19:x@thread.v2"] != 3 || got["19:new@thread.v2"] != 4 {
		t.Fatalf("favorites after pin %v", got)
	}
}

func TestSetChatPinnedRemoves(t *testing.T) {
	f := &fakeProperties{favorites: `{"48:notes":1,"19:x@thread.v2":2}`}
	c := newPropertiesClient(t, f)
	if err := c.SetChatPinned(context.Background(), "48:notes", false); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 {
		t.Fatalf("%d writes", len(f.puts))
	}
	if got := decodeFavorites(t, f.puts[0]); len(got) != 1 || got["19:x@thread.v2"] != 2 {
		t.Fatalf("favorites after unpin %v", got)
	}
}

func TestSetChatPinnedNoChangeNoWrite(t *testing.T) {
	f := &fakeProperties{favorites: `{"48:notes":1}`}
	c := newPropertiesClient(t, f)
	ctx := context.Background()
	if err := c.SetChatPinned(ctx, "48:notes", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetChatPinned(ctx, "19:other@thread.v2", false); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 0 {
		t.Fatalf("wrote %v for no change", f.puts)
	}
}

func TestSetChatPinnedFirstPin(t *testing.T) {
	f := &fakeProperties{}
	c := newPropertiesClient(t, f)
	if err := c.SetChatPinned(context.Background(), "48:notes", true); err != nil {
		t.Fatal(err)
	}
	if got := decodeFavorites(t, f.puts[0]); len(got) != 1 || got["48:notes"] != 1 {
		t.Fatalf("favorites %v", got)
	}
}

func TestChatStateErrors(t *testing.T) {
	ctx := context.Background()
	f := &fakeProperties{status: http.StatusForbidden}
	c := newPropertiesClient(t, f)
	var propErr UserPropertiesError
	if _, err := c.GetPinnedChats(ctx); !errors.As(err, &propErr) || propErr.Status != http.StatusForbidden {
		t.Errorf("403: %v", err)
	}
	f.status = http.StatusTooManyRequests
	var retry RetryableError
	if err := c.SetChatPinned(ctx, "48:notes", true); !errors.As(err, &retry) {
		t.Errorf("429: %v", err)
	}
	if err := c.SetChatPinned(ctx, " ", true); err == nil {
		t.Error("empty conversation id accepted")
	}
	c.Token = ""
	if _, err := c.GetPinnedChats(ctx); !errors.Is(err, ErrMissingToken) {
		t.Errorf("no token: %v", err)
	}
	var nilClient *Client
	if _, err := nilClient.GetPinnedChats(ctx); !errors.Is(err, ErrMissingHTTPClient) {
		t.Errorf("nil client: %v", err)
	}
}

func TestUserPropertiesURL(t *testing.T) {
	c := NewClient(http.DefaultClient)
	c.MessagesURL = "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations"
	if got := c.userPropertiesURL("favorites"); got != "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/properties?name=favorites" {
		t.Errorf("url %s", got)
	}
}
