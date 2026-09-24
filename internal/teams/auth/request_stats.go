package auth

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

// requestStats counts the requests every Teams client makes, by kind, so
// the bridge can report how much it asks of Teams (the poll loop logs it
// hourly).  It is process-wide: all clients share the tally.
var requestStats = struct {
	sync.Mutex
	byKind    map[string]int64
	throttled int64
}{byKind: map[string]int64{}}

// requestKind names the kind of request req is, for the tally.
func requestKind(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "other"
	}
	host := strings.ToLower(req.URL.Hostname())
	path := strings.ToLower(req.URL.Path)
	switch {
	case strings.HasPrefix(host, "login."):
		return "sign-in"
	case strings.HasPrefix(host, "graph."):
		return "graph"
	case strings.Contains(path, "/authz") || strings.Contains(path, "/authsvc"):
		return "skypetoken"
	case strings.Contains(host, ".asm.") || strings.HasPrefix(host, "ams") ||
		strings.Contains(host, ".ams.") || strings.Contains(path, "/objects/"):
		return "media"
	case strings.HasSuffix(path, "/messages") || strings.Contains(path, "/messages/"):
		return "messages"
	case strings.Contains(path, "consumptionhorizons"):
		return "horizons"
	case strings.HasSuffix(path, "/conversations"):
		return "conversations"
	case strings.Contains(path, "/threads/"):
		return "threads"
	case strings.Contains(path, "/endpoints") || strings.Contains(path, "/poll"):
		return "endpoints"
	case strings.Contains(path, "/properties"):
		return "properties"
	}
	return "other"
}

// countRequest records one request of req's kind, and a 429 answer to it.
func countRequest(req *http.Request, resp *http.Response) {
	kind := requestKind(req)
	requestStats.Lock()
	defer requestStats.Unlock()
	requestStats.byKind[kind]++
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		requestStats.throttled++
	}
}

// RequestCount is the tally for one kind of request.
type RequestCount struct {
	Kind  string
	Count int64
}

// TakeRequestStats returns the requests made since the last call, busiest
// kind first, their total, and how many were answered 429, and starts a new
// tally.
func TakeRequestStats() (counts []RequestCount, total, throttled int64) {
	requestStats.Lock()
	defer requestStats.Unlock()
	for kind, n := range requestStats.byKind {
		counts = append(counts, RequestCount{Kind: kind, Count: n})
		total += n
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count != counts[j].Count {
			return counts[i].Count > counts[j].Count
		}
		return counts[i].Kind < counts[j].Kind
	})
	throttled = requestStats.throttled
	requestStats.byKind = map[string]int64{}
	requestStats.throttled = 0
	return counts, total, throttled
}
