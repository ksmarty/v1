package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNextTagPage(t *testing.T) {
	cases := []struct{ link, want string }{
		{`</v2/o/i/tags/list?last=v0.56.4&n=0>; rel="next"`, "/v2/o/i/tags/list?last=v0.56.4&n=0"},
		{`<https://ghcr.io/v2/o/i/tags/list?last=v1.0.0>; rel="next"`, "https://ghcr.io/v2/o/i/tags/list?last=v1.0.0"},
		{`</v2/o/i/tags/list?first=x>; rel="prev", </v2/o/i/tags/list?last=y>; rel="next"`, "/v2/o/i/tags/list?last=y"},
		{`</v2/o/i/tags/list?last=y>; rel="prev"`, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := nextTagPage(c.link); got != c.want {
			t.Fatalf("nextTagPage(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

// fakeRegistry serves a paginated tag list: pages are alphabetical chunks like
// ghcr.io, with the newest versions only on the LAST page. Reproduces the bug
// where reading a single page returned "some versions from the middle".
func fakeRegistry(t *testing.T, tags []string, perPage int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/o/i/tags/list", func(w http.ResponseWriter, r *http.Request) {
		last := r.URL.Query().Get("last")
		start := 0
		if last != "" {
			for i, tg := range tags {
				if tg == last {
					start = i + 1
					break
				}
			}
		}
		end := start + perPage
		if end > len(tags) {
			end = len(tags)
		}
		page := append([]string{}, tags[start:end]...)
		if end < len(tags) && len(page) > 0 {
			w.Header().Set("Link", fmt.Sprintf(`</v2/o/i/tags/list?last=%s&n=0>; rel="next"`, page[len(page)-1]))
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "o/i", "tags": page})
	})
	return httptest.NewServer(mux)
}

func TestRegistryTagsWalksAllPages(t *testing.T) {
	var tags []string
	tags = append(tags, "v0.0.1", "latest")
	// 240 alphabetical versions; the newest ones sit at the end of the list.
	for i := 2; i < 240; i++ {
		tags = append(tags, fmt.Sprintf("v0.%03d.0", i))
	}
	srv := fakeRegistry(t, tags, 100)
	defer srv.Close()

	old := registryClient
	registryClient = srv.Client()
	defer func() { registryClient = old }()

	got, err := registryTags(context.Background(), srv.URL, "o/i", "")
	if err != nil {
		t.Fatalf("registryTags: %v", err)
	}
	if len(got.Newest) != maxRegistryTags {
		t.Fatalf("got %d tags, want %d", len(got.Newest), maxRegistryTags)
	}
	if got.Newest[0] != "v0.239.0" {
		t.Fatalf("newest tag = %q, want v0.239.0 (all pages: %v)", got.Newest[0], got.Newest)
	}
	// "latest" and empties are excluded from the total.
	if got.Total != len(tags)-1 {
		t.Fatalf("total = %d, want %d", got.Total, len(tags)-1)
	}
	// Descending across the page boundary.
	if !strings.HasPrefix(got.Newest[0], "v0.239") || !strings.HasPrefix(got.Newest[1], "v0.238") {
		t.Fatalf("not newest-first across pages: %v", got.Newest)
	}
}

func TestRegistryTagsSinglePage(t *testing.T) {
	tags := []string{"v0.0.1", "v0.0.2", "latest"}
	srv := fakeRegistry(t, tags, 100)
	defer srv.Close()

	old := registryClient
	registryClient = srv.Client()
	defer func() { registryClient = old }()

	got, err := registryTags(context.Background(), srv.URL, "o/i", "")
	if err != nil {
		t.Fatalf("registryTags: %v", err)
	}
	if len(got.Newest) != 2 || got.Newest[0] != "v0.0.2" || got.Total != 2 {
		t.Fatalf("got %+v, want newest [v0.0.2 v0.0.1] total 2", got)
	}
}
