package server

import (
	"testing"
	"time"

	"v1/internal/agent"
)

// TestHubReplayMatchesLive verifies a subscriber attaching mid-run receives
// every event from the last round boundary onward: the in-flight tool call,
// the deltas after it, side effects and the final done — in order. The
// committed rounds before that boundary come from the store (the client
// converges right after the replay), so replaying them would only duplicate
// rows a re-attaching client already holds.
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

	ch, release, replayed := h.subscribe()
	defer release()
	h.close()
	var got []agent.ChatEvent
	for ev := range collect(ch, 5*time.Second) {
		got = append(got, ev)
	}
	tail := published[2:] // from the last tool_start onward
	if len(got) != len(tail) {
		t.Fatalf("replayed %d events, want %d from the last tool_start (%v)", len(got), len(tail), got)
	}
	if replayed != len(tail) {
		t.Fatalf("replay count = %d, want %d", replayed, len(tail))
	}
	for i, ev := range got {
		if ev.Type != tail[i].Type {
			t.Fatalf("event %d type = %q, want %q (full: %v)", i, ev.Type, tail[i].Type, got)
		}
	}
	if got[0].Detail != "ls" {
		t.Fatalf("replay must start at the in-flight tool call: %v", got[0])
	}
}

// TestHubReplayBeforeFirstToolStart verifies a turn that has not called a
// tool yet replays whole — there is no boundary to trim at.
func TestHubReplayBeforeFirstToolStart(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "reasoning", Text: "think"})
	h.publish(agent.ChatEvent{Type: "delta", Text: "partial"})
	ch, release, replayed := h.subscribe()
	defer release()
	h.close()
	var got []agent.ChatEvent
	for ev := range collect(ch, 5*time.Second) {
		got = append(got, ev)
	}
	if replayed != 2 || len(got) != 2 || got[0].Text != "think" || got[1].Text != "partial" {
		t.Fatalf("replay = %v (count %d), want the full pre-tool turn", got, replayed)
	}
}

// TestHubReplayStartsAtLastToolStart verifies several rounds collapse to the
// newest boundary: an earlier round's tool_start must not widen the replay.
func TestHubReplayStartsAtLastToolStart(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "tool_start", Name: "read_file", Detail: "a"})
	h.publish(agent.ChatEvent{Type: "tool_end", Name: "read_file", OK: true})
	h.publish(agent.ChatEvent{Type: "tool_start", Name: "read_file", Detail: "b"})
	h.publish(agent.ChatEvent{Type: "tool_end", Name: "read_file", OK: true})
	h.publish(agent.ChatEvent{Type: "delta", Text: "answer"})
	ch, release, replayed := h.subscribe()
	defer release()
	h.close()
	var got []agent.ChatEvent
	for ev := range collect(ch, 5*time.Second) {
		got = append(got, ev)
	}
	if replayed != 3 || len(got) != 3 || got[0].Type != "tool_start" || got[0].Detail != "b" ||
		got[1].Type != "tool_end" || got[2].Text != "answer" {
		// The last boundary is the second call's tool_start: the replay carries
		// that call and everything after it — not the first round.
		t.Fatalf("replay = %v (count %d), want [tool_start b, tool_end, delta answer]", got, replayed)
	}
	if got[0].Type != "tool_start" {
		t.Fatalf("replay must include the boundary tool_start: %v", got[0])
	}
}

// TestHubLiveAndReplayOrder verifies events published after subscribe arrive
// live after the replayed prefix, with no gap or duplication.
func TestHubLiveAndReplayOrder(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "delta", Text: "a"})
	h.publish(agent.ChatEvent{Type: "delta", Text: "b"})

	ch, release, _ := h.subscribe()
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
// wraps (more events than histSize), and that subscribe completes even when
// the caller has not started draining yet (the watch handler reads only
// after subscribe returns — the buffer must hold the whole replay).
func TestHubReplayAcrossRingWrap(t *testing.T) {
	h := newRunHub()
	for i := 0; i < h.histSize+40; i++ {
		h.publish(agent.ChatEvent{Type: "delta", Text: "x"})
	}
	ch, release, replayed := h.subscribe()
	defer release()
	h.close()
	got := 0
	for range collect(ch, 5*time.Second) {
		got++
	}
	want := h.histSize // deltas only: no tool_start to trim at, so the ring replays whole
	if got != want || replayed != want {
		t.Fatalf("replayed %d (count %d), want the newest %d of the ring", got, replayed, want)
	}
}

// TestHubClosedSubscriber verifies subscribing to a closed hub yields a
// closed channel immediately (watch handlers return without hanging).
func TestHubClosedSubscriber(t *testing.T) {
	h := newRunHub()
	h.publish(agent.ChatEvent{Type: "delta", Text: "x"})
	h.close()
	ch, release, _ := h.subscribe()
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
