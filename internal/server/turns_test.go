package server

import (
	"errors"
	"testing"
)

func TestBeginOrQueue(t *testing.T) {
	m := newTurnManager()
	q, started, _ := m.beginOrQueue("p", "s1", "first")
	if !started {
		t.Fatal("first call should start a run")
	}
	var id string
	if _, started, id = m.beginOrQueue("p", "s1", "second"); started {
		t.Fatal("second call should queue onto the active run")
	}
	if id == "" {
		t.Fatal("queued message should get an id")
	}
	if got := q.drain(); len(got) != 1 || got[0].Text != "second" {
		t.Fatalf("drain = %v, want [second]", got)
	}
	if got := q.drain(); got != nil {
		t.Fatalf("second drain = %v, want nil", got)
	}
	m.end("p", "s1")
	if _, started, _ := m.beginOrQueue("p", "s1", "third"); !started {
		t.Fatal("after end, a new run should start")
	}
}

func TestQueueReorderSteerDrain(t *testing.T) {
	m := newTurnManager()
	q, started, _ := m.beginOrQueue("p", "s1", "")
	if !started {
		t.Fatal("should start a run")
	}
	a := q.add("a")
	b := q.add("b")
	c := q.add("c")

	// list preserves order
	if got := q.list(); len(got) != 3 || got[0].Text != "a" || got[2].Text != "c" {
		t.Fatalf("list = %v", got)
	}

	// reorder with a permutation
	if err := q.reorder([]string{c, a, b}); err != nil {
		t.Fatal(err)
	}
	if got := q.list(); got[0].Text != "c" || got[1].Text != "a" || got[2].Text != "b" {
		t.Fatalf("after reorder: %v", got)
	}

	// reorder with a missing id fails and changes nothing
	if err := q.reorder([]string{c, a, "nope"}); !errors.Is(err, errNotQueued) {
		t.Fatalf("reorder err = %v", err)
	}
	if got := q.list(); got[0].Text != "c" {
		t.Fatalf("failed reorder changed the queue: %v", got)
	}

	// edit replaces text in place
	if err := q.edit(a, "a-edited"); err != nil {
		t.Fatal(err)
	}
	if got := q.list(); got[1].Text != "a-edited" {
		t.Fatalf("after edit: %v", got)
	}
	if err := q.edit("nope", "x"); !errors.Is(err, errNotQueued) {
		t.Fatalf("edit err = %v", err)
	}

	// steer moves a message out of the queue and into the steer stream
	if err := q.steer(a); err != nil {
		t.Fatal(err)
	}
	if err := q.steer("nope"); !errors.Is(err, errNotQueued) {
		t.Fatalf("steer err = %v", err)
	}
	if got := q.list(); len(got) != 2 || got[0].Text != "c" || got[1].Text != "b" {
		t.Fatalf("after steer: %v", got)
	}
	if got := q.steerDrain(); len(got) != 1 || got[0] != "a-edited" {
		t.Fatalf("steerDrain = %v, want [a-edited]", got)
	}
	if got := q.steerDrain(); got != nil {
		t.Fatalf("second steerDrain = %v, want nil", got)
	}

	// held messages are skipped by drain and stay put
	if err := q.hold(b, true); err != nil {
		t.Fatal(err)
	}
	if q.heldCount() != 1 {
		t.Fatalf("heldCount = %d", q.heldCount())
	}
	d := q.drain()
	if len(d) != 1 || d[0].Text != "c" || q.heldCount() != 1 {
		t.Fatalf("drain with held: got %v held %d", d, q.heldCount())
	}
	// releasing via hold(false) makes it drainable again
	if err := q.hold(b, false); err != nil {
		t.Fatal(err)
	}
	if err := q.hold(b, true); err != nil {
		t.Fatal(err)
	}
	// edit releases the hold
	if err := q.edit(b, "b-fixed"); err != nil {
		t.Fatal(err)
	}
	if q.heldCount() != 0 {
		t.Fatalf("heldCount after edit = %d", q.heldCount())
	}
	if err := q.hold("nope", true); !errors.Is(err, errNotQueued) {
		t.Fatalf("hold err = %v", err)
	}

	// drain returns unconsumed steers before the pending queue
	c = q.add("c") // drained away above — put it back for the steer section
	if err := q.steer(c); err != nil {
		t.Fatal(err)
	}
	got := q.drain()
	if len(got) != 2 || got[0].Text != "c" || got[1].Text != "b-fixed" {
		t.Fatalf("final drain = %v, want [c b-fixed]", got)
	}
}
