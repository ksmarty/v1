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
