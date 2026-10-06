package harness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEventQueueDrainsInOrder(t *testing.T) {
	q := NewEventQueue()
	q.Push([]Event{{Type: "a"}, {Type: "b"}})
	q.Push([]Event{{Type: "c"}})

	got, err := q.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Type != "a" || got[1].Type != "b" || got[2].Type != "c" {
		t.Fatalf("drained %+v", got)
	}
	// A drained queue is empty, not closed: the next Drain waits.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := q.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline", err)
	}
}

// The sidecar's event notifications arrive on the bridge's read loop, so a
// push must never block even when nobody is draining.
func TestEventQueuePushNeverBlocks(t *testing.T) {
	q := NewEventQueue()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			q.Push([]Event{{Type: "tick"}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Push blocked with no reader")
	}
	got, err := q.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10_000 {
		t.Fatalf("drained %d events, want 10000", len(got))
	}
}

func TestEventQueueCloseReportsError(t *testing.T) {
	q := NewEventQueue()
	q.Push([]Event{{Type: "last"}})
	q.Close(errors.New("sidecar gone"))

	got, err := q.Drain(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("first drain = %+v, %v", got, err)
	}
	if _, err := q.Drain(context.Background()); err == nil || err.Error() != "sidecar gone" {
		t.Fatalf("err = %v, want the close error", err)
	}
}

func TestEventQueueCleanClose(t *testing.T) {
	q := NewEventQueue()
	q.Close(nil)
	if _, err := q.Drain(context.Background()); err != nil {
		t.Fatalf("err = %v, want nil after a clean close", err)
	}
	// Closing twice is harmless, and a late push is dropped rather than
	// resurrecting a stopped stream.
	q.Close(errors.New("late"))
	q.Push([]Event{{Type: "late"}})
	if _, err := q.Drain(context.Background()); err != nil {
		t.Fatalf("err = %v, want the first close to win", err)
	}
}

func TestEventQueueDrainUnblocksOnPush(t *testing.T) {
	q := NewEventQueue()
	go func() {
		time.Sleep(10 * time.Millisecond)
		q.Push([]Event{{Type: "woke"}})
	}()
	got, err := q.Drain(context.Background())
	if err != nil || len(got) != 1 || got[0].Type != "woke" {
		t.Fatalf("drain = %+v, %v", got, err)
	}
}
