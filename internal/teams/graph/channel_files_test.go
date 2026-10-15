package graph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListChannelFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/filesFolder"):
			_, _ = w.Write([]byte(`{"id":"root","parentReference":{"driveId":"d1"}}`))
		case strings.Contains(r.URL.Path, "/items/root/children"):
			_, _ = w.Write([]byte(`{"value":[{"id":"f1","name":"Slides","folder":{"childCount":1},"webUrl":"https://x/Slides"},
				{"id":"s1","name":"syllabus.pdf","size":2048,"lastModifiedDateTime":"2026-10-01T12:00:00Z","lastModifiedBy":{"user":{"displayName":"Tim"}},"webUrl":"https://x/syllabus.pdf"}]}`))
		case strings.Contains(r.URL.Path, "/items/f1/children"):
			_, _ = w.Write([]byte(`{"value":[{"id":"l1","name":"week1.pptx","size":10,"webUrl":"https://x/Slides/week1.pptx"}]}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	old := graphBaseURL
	graphBaseURL = srv.URL
	defer func() { graphBaseURL = old }()
	c := NewClient(srv.Client())
	c.AccessToken = "tok"
	files, err := c.ListChannelFiles(context.Background(), "team", "19:c@thread.tacv2")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].Name != "syllabus.pdf" || files[0].ModifiedBy != "Tim" || files[0].Size != 2048 ||
		!files[1].IsFolder || files[2].Name != "week1.pptx" || files[2].Path != "Slides" {
		t.Errorf("files = %+v", files)
	}
}
