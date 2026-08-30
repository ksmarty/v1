package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBackgroundManagerLifecycle(t *testing.T) {
	m := NewBackgroundManager()
	var notified *BackgroundJob
	id, err := m.Start(t.TempDir(), "echo hello && sleep 0.2 && echo world", 10*time.Second, "sess1", func(j *BackgroundJob) { notified = j })
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("missing job id")
	}
	// A job in another session is never returned to this one.
	other, err := m.Start(t.TempDir(), "echo other", 10*time.Second, "sess2", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = other

	deadline := time.Now().Add(5 * time.Second)
	for notified == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notified == nil {
		t.Fatal("completion callback never fired")
	}
	if notified.ExitCode != 0 || !strings.Contains(notified.Output, "hello") || !strings.Contains(notified.Output, "world") {
		t.Fatalf("job = exit %d output %q", notified.ExitCode, notified.Output)
	}
	done := m.Completed("sess1")
	if len(done) != 1 || done[0].ID != id {
		t.Fatalf("Completed(sess1) = %v", done)
	}
	if got := m.Completed("sess1"); got != nil {
		t.Fatalf("second Completed(sess1) = %v, want nil", got)
	}
	if got := m.Completed("sess2"); len(got) != 1 {
		t.Fatalf("Completed(sess2) = %v, want the other job", got)
	}
}

// TestBackgroundCancelSession verifies that cancelling a session terminates
// its running detached commands quickly instead of leaving them to run on.
func TestBackgroundCancelSession(t *testing.T) {
	m := NewBackgroundManager()
	var notified *BackgroundJob
	if _, err := m.Start(t.TempDir(), "sleep 30", 60*time.Second, "sessK", func(j *BackgroundJob) { notified = j }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	m.CancelSession("sessK")
	deadline := time.Now().Add(10 * time.Second)
	for notified == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notified == nil {
		t.Fatal("cancelled job never finished")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancelled job was not killed promptly")
	}
	if notified.ExitCode == 0 {
		t.Fatalf("expected a killed (non-zero) exit code, got 0")
	}
}

// TestBackgroundKillAll verifies that shutting the manager down terminates
// every running detached command regardless of session.
func TestBackgroundKillAll(t *testing.T) {
	m := NewBackgroundManager()
	var n1, n2 *BackgroundJob
	if _, err := m.Start(t.TempDir(), "sleep 30", 60*time.Second, "sessA", func(j *BackgroundJob) { n1 = j }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.TempDir(), "sleep 30", 60*time.Second, "sessB", func(j *BackgroundJob) { n2 = j }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	m.KillAll()
	deadline := time.Now().Add(10 * time.Second)
	for (n1 == nil || n2 == nil) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n1 == nil || n2 == nil {
		t.Fatal("KillAll did not terminate both sessions' jobs")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("KillAll did not kill jobs promptly")
	}
	if n1.ExitCode == 0 || n2.ExitCode == 0 {
		t.Fatalf("expected killed (non-zero) exit codes, got %d/%d", n1.ExitCode, n2.ExitCode)
	}
}

func TestBackgroundManagerTimeout(t *testing.T) {
	m := NewBackgroundManager()
	var notified *BackgroundJob
	if _, err := m.Start(t.TempDir(), "sleep 30", 300*time.Millisecond, "s", func(j *BackgroundJob) { notified = j }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for notified == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notified == nil || !notified.TimedOut {
		t.Fatalf("expected timeout, job = %+v", notified)
	}
	if text := BackgroundResultText(notified); !strings.Contains(text, "timed out") {
		t.Fatalf("result text = %q", text)
	}
}

func TestRunCommandBackgroundTool(t *testing.T) {
	e := newTestExecutor(t)
	e.Background = NewBackgroundManager()
	var notified *BackgroundJob
	e.BackgroundNotify = func(j *BackgroundJob) { notified = j }

	res, err := e.Execute(context.Background(), "run_command_background", `{"command":"echo bg-output"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, `"status":"running"`) {
		t.Fatalf("result = %q", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for notified == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notified == nil {
		t.Fatal("completion callback never fired")
	}
	text := BackgroundResultText(notified)
	if !strings.Contains(text, "bg-output") || !strings.Contains(text, "[Background #") {
		t.Fatalf("result text = %q", text)
	}

	if _, err := e.Execute(context.Background(), "run_command_background", `{"command":""}`); err == nil {
		t.Fatal("empty command should fail")
	}
	if _, err := e.Execute(context.Background(), "run_command_background", `{"command":"echo x"}`); err != nil {
		t.Fatalf("background without a manager should fail: %v", err)
	}
}

func TestSanitizeBackgroundText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain output", "plain output"},
		{"\x1b[31mred\x1b[0m text", "red text"},
		{"\x1b]0;title\x07osc", "osc"},
		{"line1\r\nline2", "line1\nline2"},
		{"tab\there", "tab\there"},
		{"bell\x07gone", "bellgone"},
		{"nul\x00byte", "nulbyte"},
		// DEL + C1 control bytes
		{"del\x7fgone", "delgone"},
	}
	for _, tc := range cases {
		if got := sanitizeBackgroundText(tc.in); got != tc.want {
			t.Errorf("sanitizeBackgroundText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// CR alone (old Mac line endings) is stripped, not kept.
	if got := sanitizeBackgroundText("a\rb"); got != "ab" {
		t.Errorf("CR not stripped: %q", got)
	}
	// Invalid UTF-8 is replaced (one U+FFFD per run of invalid bytes).
	if got := sanitizeBackgroundText("bad\xff\xfebytes"); got != "bad\uFFFDbytes" {
		t.Errorf("invalid utf8 not replaced: %q", got)
	}
}
