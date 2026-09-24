package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestKind(t *testing.T) {
	cases := map[string]string{
		"https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/19:a@thread.v2/messages?pageSize=200": "messages",
		"https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/48:notes/messages/1790":               "messages",
		"https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations?view=msnp24Equivalent":                "conversations",
		"https://amer.ng.msg.teams.microsoft.com/v1/threads/19:a@thread.v2/consumptionhorizons":                  "horizons",
		"https://amer.ng.msg.teams.microsoft.com/v1/threads/19:a@thread.v2/members":                              "threads",
		"https://teams.microsoft.com/api/authsvc/v1.0/authz":                                                     "skypetoken",
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/token":                                             "sign-in",
		"https://graph.microsoft.com/v1.0/me/photo/$value":                                                       "graph",
		"https://us-api.asm.skype.com/v1/objects/0-eus-d1-abc/views/imgo":                                        "media",
		"https://amer.ng.msg.teams.microsoft.com/v1/users/ME/endpoints/SELF/subscriptions/0/poll":                "endpoints",
		"https://example.com/elsewhere":                                                                          "other",
	}
	for raw, want := range cases {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		if got := requestKind(req); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

func TestTakeRequestStats(t *testing.T) {
	TakeRequestStats() // start clean
	msgs := httptest.NewRequest(http.MethodGet, "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations/19:a@thread.v2/messages", nil)
	list := httptest.NewRequest(http.MethodGet, "https://amer.ng.msg.teams.microsoft.com/v1/users/ME/conversations", nil)
	countRequest(msgs, &http.Response{StatusCode: 200})
	countRequest(msgs, &http.Response{StatusCode: 429})
	countRequest(list, nil) // a transport error still counts as a request

	counts, total, throttled := TakeRequestStats()
	if total != 3 || throttled != 1 || len(counts) != 2 ||
		counts[0] != (RequestCount{"messages", 2}) || counts[1] != (RequestCount{"conversations", 1}) {
		t.Fatalf("counts %v total %d throttled %d", counts, total, throttled)
	}
	if _, total, throttled := TakeRequestStats(); total != 0 || throttled != 0 {
		t.Fatal("tally not reset")
	}
}
