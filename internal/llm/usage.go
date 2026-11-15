package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProviderUsage is a provider's own rate-limit accounting for the signed-in
// key: one window per bucket the provider meters (a rolling few-hours bucket,
// a weekly one, a monthly one, ...). It is what a subscription exposes instead
// of per-token pricing, so the UI can show how much of the plan is spent.
type ProviderUsage struct {
	// ProviderID is the saved provider the usage belongs to, when known.
	ProviderID string `json:"providerId,omitempty"`
	// Provider names the endpoint family that answered (e.g. "opencode-zen").
	Provider string `json:"provider,omitempty"`
	// Windows are the metered buckets, widest-last: the same order the rings
	// draw them in, outermost first.
	Windows []UsageWindow `json:"windows"`
	// FetchedAt is when this reading was taken upstream (not when it was served
	// from cache) — the client uses it to decide whether to refresh.
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
}

// UsageWindow is one metered bucket. Percent is how much of the allowance is
// spent, 0..100.
type UsageWindow struct {
	// Key is the provider's own name for the window ("rolling", "weekly", ...).
	Key string `json:"key"`
	// Label is the human name for it ("5 hour", "Weekly", ...).
	Label string `json:"label"`
	// Percent is the spent share of the window, clamped to 0..100.
	Percent float64 `json:"percent"`
	// Status is the provider's own verdict ("ok", "exhausted", ...), when sent.
	Status string `json:"status,omitempty"`
	// ResetsAt is when the window rolls over, when the provider reports it.
	ResetsAt time.Time `json:"resetsAt,omitzero"`
}

// ErrUsageUnsupported means the endpoint publishes no usage at all, which is a
// normal state (most providers only bill tokens) rather than a failure: the UI
// disables the button for it instead of reporting an error.
var ErrUsageUnsupported = errors.New("provider reports no usage")

// usageDriver describes one family of usage endpoint: where the reading lives
// and how to read it.
//
// This registry is the whole extension point for a new provider. Adding Claude
// Code or Codex means adding one entry here (a host predicate, a path and a
// small parser) — no route, client or UI change. A provider that speaks the
// OpenCode shape needs not even that: it is matched by host, so any base URL on
// opencode.ai gets usage for free once its key is saved as a provider.
type usageDriver struct {
	// ID names the driver in the response and in logs.
	ID string
	// Name is the human label shown next to the reading.
	Name string
	// Match reports whether this driver serves the endpoint. Matching on host
	// rather than on the provider id is deliberate: one API is served under
	// several paths (/zen/v1, /zen/go/v1) and the id is user-editable.
	Match func(baseURL string) bool
	// Path is appended to the base URL.
	Path string
	// Parse turns a 2xx body into windows. It returns ErrUsageUnsupported when
	// the body is not a usage document at all.
	Parse func(body []byte) ([]UsageWindow, error)
}

// usageDrivers is ordered: the first match wins.
var usageDrivers = []usageDriver{
	{
		ID:    "opencode-zen",
		Name:  "OpenCode Zen",
		Match: func(baseURL string) bool { return usageHost(baseURL) == "opencode.ai" },
		Path:  "/usage",
		Parse: parseOpenCodeUsage,
	},
}

// usageDriverFor returns the driver serving baseURL, or nil when the endpoint
// publishes no usage.
func usageDriverFor(baseURL string) *usageDriver {
	for i := range usageDrivers {
		if usageDrivers[i].Match != nil && usageDrivers[i].Match(baseURL) {
			return &usageDrivers[i]
		}
	}
	return nil
}

func usageHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// openCodeUsage is the OpenCode Zen rate-limit document:
//
//	{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"..."},
//	          "weekly":{...},"monthly":{...}}}
//
// "rolling" is the five-hour bucket — the endpoint names the span by how it
// rolls rather than by its length — so the display name is ours, not theirs.
type openCodeUsage struct {
	Usage map[string]struct {
		Status   string    `json:"status"`
		Percent  float64   `json:"percent"`
		ResetsAt time.Time `json:"resetsAt"`
	} `json:"usage"`
}

func parseOpenCodeUsage(body []byte) ([]UsageWindow, error) {
	var doc openCodeUsage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	// Widest last, matching the ring order (5 hour outermost).
	order := []struct{ key, label string }{
		{"rolling", "5 hour"},
		{"weekly", "Weekly"},
		{"monthly", "Monthly"},
	}
	out := make([]UsageWindow, 0, len(order))
	for _, o := range order {
		lim, ok := doc.Usage[o.key]
		if !ok {
			continue
		}
		out = append(out, UsageWindow{
			Key:      o.key,
			Label:    o.label,
			Percent:  clampPercent(lim.Percent),
			Status:   lim.Status,
			ResetsAt: lim.ResetsAt,
		})
	}
	if len(out) == 0 {
		return nil, ErrUsageUnsupported
	}
	return out, nil
}

func clampPercent(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}

// FetchUsage reads the endpoint's current rate-limit usage. It returns
// ErrUsageUnsupported when the provider publishes none.
//
// The request goes through the Client (not a bare http.Get) because these
// endpoints share the chat endpoint's required headers: opencode.ai answers
// 400 without x-opencode-session and x-opencode-client.
func (c *Client) FetchUsage(ctx context.Context) (*ProviderUsage, error) {
	d := usageDriverFor(c.BaseURL)
	if d == nil {
		return nil, ErrUsageUnsupported
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+d.Path, nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("Accept", "application/json")
	c.applyEndpointHeaders(req)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 404/405 means the endpoint exists but has no usage document (the paid
		// zen base answers that way): that is "unsupported", not a failure.
		// Anything else — a rejected key, a server error — is worth surfacing,
		// because the fix is on the user's side.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return nil, ErrUsageUnsupported
		}
		return nil, fmt.Errorf("HTTP %d from %s%s", resp.StatusCode, c.BaseURL, d.Path)
	}
	windows, err := d.Parse(body)
	if err != nil {
		if errors.Is(err, ErrUsageUnsupported) {
			return nil, ErrUsageUnsupported
		}
		return nil, fmt.Errorf("unreadable usage from %s%s: %w", c.BaseURL, d.Path, err)
	}
	return &ProviderUsage{Provider: d.Name, Windows: windows, FetchedAt: time.Now()}, nil
}
