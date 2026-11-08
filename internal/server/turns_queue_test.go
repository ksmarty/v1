package server

import "testing"

// A turn that leaves queued work does not end the run: the notification and the
// client's idle state must wait for the turn that really ends it, or the alert
// says "finished" for a chat that keeps working. `remaining` is what that
// decision is based on, so it has to count both lists.
func TestTurnQueueRemainingCountsQueuedAndSteers(t *testing.T) {
	q := &turnQueue{}
	if n := q.remaining(); n != 0 {
		t.Fatalf("empty queue reports %d", n)
	}
	q.add("queued while the turn ran", nil)
	if n := q.remaining(); n != 1 {
		t.Fatalf("queued message not counted: %d", n)
	}
	id := q.add("steer me", nil)
	if err := q.steer(id); err != nil {
		t.Fatal(err)
	}
	if n := q.remaining(); n != 2 {
		t.Fatalf("steer not counted: %d", n)
	}
	if msgs := q.drain(); len(msgs) != 2 {
		t.Fatalf("drain returned %d messages", len(msgs))
	}
	if n := q.remaining(); n != 0 {
		t.Fatalf("drained queue still reports %d", n)
	}
}
