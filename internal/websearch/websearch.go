// Package websearch queries the LangSearch web search API.
//
// v1 ships the tool but no key, and LangSearch is the only provider wired up.
// The key is a per-user setting: until one is present the tool is not offered
// to the model at all, so it can never be called and fail.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// endpoint is the LangSearch web-search API. It is a variable so tests can
// point it at a local server.
var endpoint = "https://api.langsearch.com/v1/web-search"

// maxResults caps a single query. The API accepts more, but every result is
// tokens in the transcript and nobody reads the tail of a 50-result list.
const maxResults = 20

// Result is one web page.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Summary string `json:"summary,omitempty"`
	Site    string `json:"site,omitempty"`
	Date    string `json:"date,omitempty"`
}

// Search runs one query and returns the pages, most relevant first.
func Search(ctx context.Context, apiKey, query string, count int) ([]Result, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("no LangSearch API key is configured: add one in Settings, under Tools & permissions")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if count <= 0 {
		count = 10
	}
	if count > maxResults {
		count = maxResults
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"freshness": "noLimit",
		"summary":   true,
		"count":     count,
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the search provider: %w", err)
	}
	defer resp.Body.Close()
	// Bound what a broken or hostile endpoint can push into the transcript.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// Parse before checking the status: the provider explains its refusals in
	// the body, and that message is more useful than a bare status code.
	var payload struct {
		Code    jsonInt `json:"code"`
		Msg     string  `json:"msg"`
		Message string  `json:"message"`
		Data    struct {
			WebPages struct {
				Value []struct {
					Name          string `json:"name"`
					URL           string `json:"url"`
					Snippet       string `json:"snippet"`
					Summary       string `json:"summary"`
					SiteName      string `json:"siteName"`
					DatePublished string `json:"datePublished"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("LangSearch returned a response that is not JSON: %w", err)
	}
	// The API uses `msg` on some responses and `message` on others.
	providerMsg := payload.Msg
	if providerMsg == "" {
		providerMsg = payload.Message
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("LangSearch rejected the API key (HTTP %d); replace it in Settings", resp.StatusCode)
	default:
		if providerMsg != "" {
			return nil, fmt.Errorf("LangSearch returned HTTP %d: %s", resp.StatusCode, providerMsg)
		}
		return nil, fmt.Errorf("LangSearch returned HTTP %d: %s", resp.StatusCode, Truncate(string(raw), 200))
	}

	// Success is 200; treat 0 as success too so a provider that omits the field
	// does not read as an error.
	if payload.Code != 0 && payload.Code != 200 {
		msg := providerMsg
		if msg == "" {
			msg = "no message"
		}
		return nil, fmt.Errorf("LangSearch error %d: %s", int(payload.Code), msg)
	}

	out := make([]Result, 0, len(payload.Data.WebPages.Value))
	for _, p := range payload.Data.WebPages.Value {
		if strings.TrimSpace(p.URL) == "" {
			continue
		}
		out = append(out, Result{
			Title:   strings.TrimSpace(p.Name),
			URL:     strings.TrimSpace(p.URL),
			Snippet: p.Snippet,
			Summary: p.Summary,
			Site:    strings.TrimSpace(p.SiteName),
			Date:    strings.TrimSpace(p.DatePublished),
		})
	}
	return out, nil
}

// jsonInt accepts a number or a quoted number. The API returns its error codes
// as strings ("401") but its success code as a number (200); decoding straight
// into an int fails on the quoted form, which would report a clear provider
// error as "response is not JSON". An unparseable value reads as 0, which is
// treated as success — the HTTP status still guards that path.
type jsonInt int

func (n *jsonInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	v, err := strconv.Atoi(s)
	if err != nil {
		*n = 0
		return nil
	}
	*n = jsonInt(v)
	return nil
}

// Format renders results for the model. Every entry carries its URL: an answer
// the user cannot check is worse than no answer, and the model needs the source
// to cite it.
func Format(query string, results []Result) string {
	if len(results) == 0 {
		return fmt.Sprintf("No web results for %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Web results for %q (%d):\n", query, len(results))
	for i, r := range results {
		title := r.Title
		if title == "" {
			title = r.URL
		}
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, title, r.URL)
		if text := description(r); text != "" {
			fmt.Fprintf(&b, "   %s\n", text)
		}
		if r.Date != "" {
			fmt.Fprintf(&b, "   (published %s)\n", r.Date)
		}
	}
	return b.String()
}

// description prefers the API's summary and falls back to the snippet, which
// is often the same text truncated.
func description(r Result) string {
	text := strings.TrimSpace(r.Summary)
	if text == "" {
		text = strings.TrimSpace(r.Snippet)
	}
	return Truncate(text, 600)
}

// Truncate collapses whitespace and cuts to n runes, so a multi-byte character
// is never split in half.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
