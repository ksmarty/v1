package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve points the package at a test server for the duration of one test.
func serve(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := endpoint
	endpoint = srv.URL
	t.Cleanup(func() { endpoint = old })
}

func TestSearchParsesResults(t *testing.T) {
	var gotAuth, gotBody string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": 200, "msg": null,
			"data": { "_type": "SearchResponse", "webPages": { "value": [
				{"name": "Go", "url": "https://go.dev", "snippet": "the language", "summary": "Go is a language.", "siteName": "go.dev", "datePublished": "2024-01-02"},
				{"name": "No URL", "url": "", "snippet": "dropped"}
			] } }
		}`))
	})

	results, err := Search(context.Background(), "  secret-key  ", "golang", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("auth header = %q, want the trimmed key", gotAuth)
	}
	for _, want := range []string{`"query":"golang"`, `"count":5`, `"summary":true`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %s does not contain %s", gotBody, want)
		}
	}
	// The entry with no URL is dropped: it cannot be cited or opened.
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].URL != "https://go.dev" || results[0].Title != "Go" {
		t.Fatalf("unexpected first result %+v", results[0])
	}
	// The live API dates results with datePublished; reading the wrong key here
	// silently drops the date from every citation.
	if results[0].Date != "2024-01-02" {
		t.Fatalf("date = %q, want it read from datePublished", results[0].Date)
	}
}

// The live API returns error codes as quoted strings while its success code is
// a bare number. Decoding straight into an int fails on the quoted form and the
// failure reads as "response is not JSON", which hides the real problem.
func TestSearchAcceptsAQuotedErrorCode(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"429","message":"too many requests"}`))
	})
	_, err := Search(context.Background(), "k", "q", 5)
	if err == nil || !strings.Contains(err.Error(), "too many requests") {
		t.Fatalf("a quoted error code should still surface the provider message, got %v", err)
	}
}

// Error responses name the message field `message`, success responses use `msg`.
func TestSearchReadsTheMessageField(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"400","message":"The API KEY is missing"}`))
	})
	_, err := Search(context.Background(), "k", "q", 5)
	if err == nil || !strings.Contains(err.Error(), "API KEY is missing") {
		t.Fatalf("error should carry the provider message field, got %v", err)
	}
}

func TestSearchRejectsAMissingKeyBeforeCalling(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was made with no API key configured")
	})
	if _, err := Search(context.Background(), "   ", "anything", 5); err == nil {
		t.Fatal("Search with a blank key should fail")
	}
}

func TestSearchExplainsARejectedKey(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"msg":"invalid api key"}`))
	})
	_, err := Search(context.Background(), "wrong", "q", 5)
	if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("error should name the key as the problem, got %v", err)
	}
}

func TestSearchSurfacesAProviderError(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":429,"msg":"rate limited"}`))
	})
	_, err := Search(context.Background(), "k", "q", 5)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error should carry the provider message, got %v", err)
	}
}

// A non-JSON body must not be reported as an empty result set: the model would
// then answer from nothing and sound certain.
func TestSearchRejectsANonJSONBody(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>maintenance</html>"))
	})
	if _, err := Search(context.Background(), "k", "q", 5); err == nil {
		t.Fatal("a non-JSON body should be an error")
	}
}

func TestSearchCapsTheCount(t *testing.T) {
	var gotBody string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"code":200,"data":{"webPages":{"value":[]}}}`))
	})
	if _, err := Search(context.Background(), "k", "q", 500); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(gotBody, `"count":20`) {
		t.Fatalf("count should be capped at %d, body was %s", maxResults, gotBody)
	}
}

func TestFormatKeepsEveryURLAndCitesSources(t *testing.T) {
	out := Format("go generics", []Result{
		{Title: "Generics", URL: "https://go.dev/g", Summary: "The generics proposal."},
		{URL: "https://example.com/x", Snippet: "  spaced   out  "},
	})
	if !strings.Contains(out, "1. Generics") || !strings.Contains(out, "https://go.dev/g") {
		t.Fatalf("first result missing from output:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/x") {
		t.Fatalf("a result with no title should still show its URL:\n%s", out)
	}
	if !strings.Contains(out, "spaced out") {
		t.Fatalf("the snippet should be whitespace-collapsed:\n%s", out)
	}
}

func TestFormatSaysSoWhenNothingWasFound(t *testing.T) {
	if out := Format("q", nil); !strings.Contains(out, "No web results") {
		t.Fatalf("empty results should be stated plainly, got %q", out)
	}
}

func TestTruncateDoesNotSplitARune(t *testing.T) {
	got := Truncate(strings.Repeat("é", 10), 3)
	if !strings.HasPrefix(got, "ééé") {
		t.Fatalf("Truncate broke a multi-byte character: %q", got)
	}
	if strings.Contains(got, "\ufffd") {
		t.Fatalf("Truncate produced a replacement character: %q", got)
	}
}
