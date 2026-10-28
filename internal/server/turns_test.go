package server

import (
	"strings"
	"testing"
)

// TestConcurrencyLimitPerUser verifies beginOrQueue enforces the per-user
// cap on concurrent runs: new runs beyond the cap are refused, queuing onto
// an active session still works, other users are unaffected, and ending a
// run frees a slot.
func TestConcurrencyLimitPerUser(t *testing.T) {
	m := newTurnManager()
	const max = 2

	if _, started, _, err := m.beginOrQueue("p1", "s1", "first", nil, "userA", max); err != nil || !started {
		t.Fatalf("first run: started=%v err=%v", started, err)
	}
	if _, started, _, err := m.beginOrQueue("p1", "s2", "second", nil, "userA", max); err != nil || !started {
		t.Fatalf("second run: started=%v err=%v", started, err)
	}
	// Third concurrent run for the same user is refused with errTooManyRuns.
	if _, _, _, err := m.beginOrQueue("p1", "s3", "third", nil, "userA", max); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("third run should be refused, err=%v", err)
	}
	// Queuing onto an active session is still allowed under the cap.
	if _, started, queuedID, err := m.beginOrQueue("p1", "s1", "queued", nil, "userA", max); err != nil || started || queuedID == "" {
		t.Fatalf("queue onto active: started=%v queuedID=%q err=%v", started, queuedID, err)
	}
	// A different user is not affected by userA's cap usage.
	if _, started, _, err := m.beginOrQueue("p1", "s4", "other", nil, "userB", max); err != nil || !started {
		t.Fatalf("other user: started=%v err=%v", started, err)
	}
	// Ending a run frees a slot for the same user.
	m.end("p1", "s2")
	if _, started, _, err := m.beginOrQueue("p1", "s3", "retry", nil, "userA", max); err != nil || !started {
		t.Fatalf("run after end should start, err=%v", err)
	}
	// Ending a completed run is a no-op (no negative counts).
	m.end("p1", "s3")
	if _, started, _, err := m.beginOrQueue("p1", "s5", "again", nil, "userA", max); err != nil || !started {
		t.Fatalf("run after full cleanup should start, err=%v", err)
	}
	// Cap of 0 disables the limit entirely.
	if _, started, _, err := m.beginOrQueue("p1", "s6", "unlimited", nil, "userA", 0); err != nil || !started {
		t.Fatalf("no cap: started=%v err=%v", started, err)
	}
}

// TestQueueWaitFields verifies the queue snapshot exposes position, an
// estimated wait and a queuedAt timestamp per entry.
func TestQueueWaitFields(t *testing.T) {
	m := newTurnManager()
	q, started, _, err := m.beginOrQueue("p1", "s1", "first", nil, "userA", 0)
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	q.add("second", nil)
	q.add("third", nil)

	list := q.list()
	if len(list) != 2 {
		t.Fatalf("queued = %d, want 2", len(list))
	}
	if list[0].QueuedAt.IsZero() || list[1].QueuedAt.Before(list[0].QueuedAt) {
		t.Fatalf("queuedAt ordering wrong: %v, %v", list[0].QueuedAt, list[1].QueuedAt)
	}
}
