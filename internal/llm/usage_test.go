package llm

import (
	"errors"
	"testing"
)

func TestUsageDriverFor(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string // driver name, "" when unsupported
	}{
		{"https://opencode.ai/zen/go/v1", "OpenCode Zen"},
		{"https://opencode.ai/zen/go/v1/", "OpenCode Zen"},
		{"https://opencode.ai/zen/v1", "OpenCode Zen"},
		{"https://api.anthropic.com/v1", ""},
		{"https://api.openai.com/v1", ""},
		{"", ""},
	}
	for _, c := range cases {
		d := usageDriverFor(c.baseURL)
		got := ""
		if d != nil {
			got = d.Name
		}
		if got != c.want {
			t.Errorf("usageDriverFor(%q) = %q, want %q", c.baseURL, got, c.want)
		}
	}
}

func TestParseOpenCodeUsage(t *testing.T) {
	body := []byte(`{"usage":{
		"rolling":{"status":"ok","percent":0,"resetsAt":"2026-11-15T06:53:34.000Z"},
		"weekly":{"status":"ok","percent":17,"resetsAt":"2026-11-16T00:00:00.000Z"},
		"monthly":{"status":"exhausted","percent":125,"resetsAt":"2026-11-23T19:25:23.000Z"}
	}}`)
	windows, err := parseOpenCodeUsage(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3", len(windows))
	}
	want := []struct {
		key, label string
		percent    float64
		status     string
	}{
		{"rolling", "5 hour", 0, "ok"},
		{"weekly", "Weekly", 17, "ok"},
		// Percent is clamped: the rings cannot draw more than a full circle.
		{"monthly", "Monthly", 100, "exhausted"},
	}
	for i, w := range want {
		got := windows[i]
		if got.Key != w.key || got.Label != w.label || got.Percent != w.percent || got.Status != w.status {
			t.Errorf("window %d = %+v, want key=%s label=%s percent=%v status=%s", i, got, w.key, w.label, w.percent, w.status)
		}
		if got.ResetsAt.IsZero() {
			t.Errorf("window %d has no reset time", i)
		}
	}
}

func TestParseOpenCodeUsagePartial(t *testing.T) {
	// A bucket the provider stopped sending must not blank the others.
	windows, err := parseOpenCodeUsage([]byte(`{"usage":{"weekly":{"percent":40}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "weekly" || windows[0].Percent != 40 {
		t.Fatalf("got %+v", windows)
	}
}

func TestParseOpenCodeUsageRejectsOtherDocuments(t *testing.T) {
	// Valid JSON without any bucket means "this endpoint has no usage"
	// (a 200 from a gateway), which the caller reports as unsupported.
	for _, body := range []string{
		`{}`,
		`{"usage":{}}`,
		`{"data":[]}`,
	} {
		if _, err := parseOpenCodeUsage([]byte(body)); !errors.Is(err, ErrUsageUnsupported) {
			t.Errorf("parse(%s) err = %v, want ErrUsageUnsupported", body, err)
		}
	}
	// A body that is not JSON at all is a real failure (an HTML login wall,
	// say): the button stays live and shows the reason instead of going
	// quietly dead.
	if _, err := parseOpenCodeUsage([]byte(`not json`)); err == nil || errors.Is(err, ErrUsageUnsupported) {
		t.Errorf("parse(not json) err = %v, want a parse error", err)
	}
}
