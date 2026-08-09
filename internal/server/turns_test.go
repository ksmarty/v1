package server

import "testing"

func TestBeginOrQueue(t *testing.T) {
	m := newTurnManager()
	q, started := m.beginOrQueue("p", "first")
	if !started {
		t.Fatal("first call should start a run")
	}
	if _, started := m.beginOrQueue("p", "second"); started {
		t.Fatal("second call should queue onto the active run")
	}
	if got := q.drain(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("drain = %v, want [second]", got)
	}
	if got := q.drain(); got != nil {
		t.Fatalf("second drain = %v, want nil", got)
	}
	m.end("p")
	if _, started := m.beginOrQueue("p", "third"); !started {
		t.Fatal("after end, a new run should start")
	}
}
