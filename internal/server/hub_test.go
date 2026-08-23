package server

import (
	"testing"
	"time"

	"v1/internal/agent"
)

// TestHubReplayMatchesLive verifies a subscriber attaching mid-run receives
// the same events a listener present from the start received: deltas,
// reasoning, tool_start/end, side effects and the final done — in order.
func TestHubReplayMatchesLive(t *testing.T) {
	h := newRunHub()
	published := []agent.ChatEvent{
		{Type: "delta", Text: "Hello "},
		{Type: "delta", Text: "world"},
		{Type: "tool_start", Name: "run_command", Detail: "ls"},
		{Type: "tool_end", Name: "run_command", OK: true, Detail: "ok"},
		{Type: "delta", Text: "done"},

		{Type: "todos", Todos: nil},
		{Type: "done", Usage: &agent.Usage{}},
	}
	for _, ev := range published {
		h.publish(ev)
	}

	ch, release := h.subscribe()
	defer release()
	h.close()
	var got []agent.ChatEvent
	for ev := range collect(ch, 5*time.Second) {
		got = append(got, ev)
	}
	if len(got) != len(published) {
		t.Fatalf("replayed %d events, want %d (%v)", len(got), len(published), got)
	}
	for i, ev := range got {
		if ev.Type != published[i].Type {
			t.Fatalf("event %d type = %q, want %q (full: %v)", i, ev.Type, published[i].Type, got)
		}
	}
	if got[0].Text != "Hello " || got[1].Text != "world" {
		t.Fatalf("delta text lost in replay: %v", got[:2])
	}
}

// TestHubLiveAndReplayOrder verifies events published after subscribe arrive
// live after the replayed prefix, with no gap or duplication.
func TestHubLiveAndReplayOrder(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "delta", Text: "a"})
	h.publish(agent.ChatEvent{Type: "delta", Text: "b"})

	ch, release := h.subscribe()
	defer release()
	// consume the replay concurrently so the live publish below can deliver
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.publish(agent.ChatEvent{Type: "delta", Text: "c"})
		h.close()
	}()

	var got []string
	for ev := range collect(ch, 5*time.Second) {
		got = append(got, ev.Text)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("sequence = %v, want [a b c]", got)
	}
}

// TestHubNoToolStartReset verifies the accumulated text/reasoning survive a
// tool boundary (the old behavior cleared them and lost pre-tool content).
func TestHubNoToolStartReset(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "delta", Text: "thinking first"})
	h.publish(agent.ChatEvent{Type: "reasoning", Text: "hidden chain of thought"})
	h.publish(agent.ChatEvent{Type: "tool_start", Tool: "run_command", Detail: "go build"})
	h.mu.Lock()
	text, reasoning := h.text, h.reasoning
	h.mu.Unlock()
	if text != "thinking first" || reasoning != "hidden chain of thought" {
		t.Fatalf("tool_start wiped the buffer: text=%q reasoning=%q", text, reasoning)
	}
}

// TestHubReplayAcrossRingWrap verifies replay stays ordered once the ring
// wraps (more events than histSize).
func TestHubReplayAcrossRingWrap(t *testing.T) {
	h := newRunHub()
	for i := 0; i < h.histSize+40; i++ {
		h.publish(agent.ChatEvent{Type: "delta", Text: "x"})
	}
	ch, release := h.subscribe()
	defer release()
	h.close()
	got := 0
	for range collect(ch, 5*time.Second) {
		got++
	}
	if got != h.histSize {
		t.Fatalf("replayed %d events, want the newest %d of the ring", got, h.histSize)
	}
}

// TestHubClosedSubscriber verifies subscribing to a closed hub yields a
// closed channel immediately (watch handlers return without hanging).
func TestHubClosedSubscriber(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "delta", Text: "x"})
	h.close()
	ch, release := h.subscribe()
	defer release()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("closed hub should not deliver events")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed hub channel never closed")
	}
}

// collect gathers sent values until the channel closes or the timeout hits.
func collect[T any](ch <-chan T, timeout time.Duration) <-chan T {
	out := make(chan T)
	go func() {
		timer := time.After(timeout)
		for {
			select {
			case v, ok := <-ch:
				if !ok {
					close(out)
					return
				}
				out <- v
			case <-timer:
				close(out)
				return
			}
		}
	}()
	return out
}
