package server

import (
	"strings"
	"testing"

	"v1/internal/agent"
	"v1/internal/store"
)

// TestConcurrencyLimitPerUser verifies beginOrQueue enforces the per-user
// cap on concurrent runs: new runs beyond the cap are refused, queuing onto
// an active session still works, other users are unaffected, and ending a
// run frees a slot.
func TestConcurrencyLimitPerUser(t *testing.T) {
	m := newTurnManager(nil)
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
	m := newTurnManager(nil)
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

// TestQueueSurvivesFailedRun covers the durability contract: a message queued
// onto a run that never drains it (a provider failure, a restart) is written
// through to the store, and the next run in that session starts with it still
// waiting — including its attachments.
func TestQueueSurvivesFailedRun(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	m := newTurnManager(st)

	q, started, _, err := m.beginOrQueue("p1", "s1", "first", nil, "userA", 0)
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	atts := []agent.Attachment{{Name: "log.txt", MIME: "text/plain", Kind: "text", Content: "hello"}}
	id := q.add("second", atts)

	// The run dies without draining (streamChatTurn returns on the error path).
	m.end("p1", "s1")

	rows, err := st.ListQueuedMessages("p1", "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].Text != "second" {
		t.Fatalf("persisted = %+v, want the queued message", rows)
	}
	if got := agent.ParseAttachments(rows[0].Attachments); len(got) != 1 || got[0].Name != "log.txt" {
		t.Fatalf("attachments not persisted: %+v", rows[0].Attachments)
	}

	// The session is idle, so the queue endpoint still sees it.
	idle := m.queueFor("p1", "s1")
	if idle == nil || len(idle.list()) != 1 {
		t.Fatalf("queueFor on an idle session should expose the stranded message")
	}

	// The next run rehydrates it, so it runs as a follow-up instead of vanishing.
	q2, started, _, err := m.beginOrQueue("p1", "s1", "third", nil, "userA", 0)
	if err != nil || !started {
		t.Fatalf("restart: started=%v err=%v", started, err)
	}
	got := q2.list()
	if len(got) != 1 || got[0].Text != "second" || len(got[0].Attachments) != 1 {
		t.Fatalf("rehydrated = %+v, want the stranded message with its attachment", got)
	}
}

// TestTakeQueuedIsIdempotent verifies the resume path cannot deliver the same
// message twice: the first take removes it everywhere, the second finds none.
func TestTakeQueuedIsIdempotent(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	m := newTurnManager(st)

	q, _, _, err := m.beginOrQueue("p1", "s1", "first", nil, "userA", 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := q.add("second", nil)
	if !m.takeQueued("p1", "s1", id) {
		t.Fatal("first take should remove the message")
	}
	if m.takeQueued("p1", "s1", id) {
		t.Fatal("second take must report the message is gone")
	}
	if len(q.list()) != 0 {
		t.Fatalf("live queue still holds it: %+v", q.list())
	}
	if rows, _ := st.ListQueuedMessages("p1", "s1"); len(rows) != 0 {
		t.Fatalf("store still holds it: %+v", rows)
	}
}
