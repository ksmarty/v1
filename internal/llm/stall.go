package llm

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// streamStallTimeout bounds the gap between two bytes on a provider stream.
// Real token streams never go silent anywhere near this long — even the
// slowest reasoning models emit bytes as they think — so a gap this big means
// a half-open connection: the peer is gone, and without a guard the read
// (bufio.Scanner has no deadline) blocks until the HTTP client's whole-request
// timeout, leaving the run "running" with no output for the rest of the
// client's idle window and beyond.
const streamStallTimeout = 180 * time.Second

// stallChunk is one read result handed from the producer goroutine to Read.
type stallChunk struct {
	data []byte
	err  error
}

// stallReader wraps a stream body so a read that would block past
// streamStallTimeout fails with a stall error instead of hanging forever.
// Reads go through a producer goroutine because the wrapped body offers no
// per-read deadline; when the deadline fires, the reader flags itself dead
// for subsequent calls and closes stop so the producer drains promptly once
// the caller closes the body.
type stallReader struct {
	body     io.Reader
	results  chan stallChunk
	stop     chan struct{}
	leftover []byte
	dead     atomic.Bool
	timer    *time.Timer
	once     sync.Once
}

// withStallTimeout wraps a response body for reading.
func withStallTimeout(r io.Reader) *stallReader {
	sr := &stallReader{
		body:    r,
		results: make(chan stallChunk),
		stop:    make(chan struct{}),
		timer:   time.NewTimer(streamStallTimeout),
	}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := sr.body.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case sr.results <- stallChunk{data: chunk}:
				case <-sr.stop:
					return
				}
			}
			if err != nil {
				select {
				case sr.results <- stallChunk{err: err}:
				case <-sr.stop:
				}
				return
			}
		}
	}()
	return sr
}

// close terminates the producer: after close, a body Read that eventually
// unblocks sees stop closed and exits instead of blocking on its send forever
// (a successful stream ends without the consumer reading the producer's final
// EOF).
func (sr *stallReader) close() {
	sr.once.Do(func() { close(sr.stop) })
	sr.timer.Stop()
}

func (sr *stallReader) Read(p []byte) (int, error) {
	if sr.dead.Load() {
		return 0, fmt.Errorf("stream stalled: no data for %s", streamStallTimeout)
	}
	if n := copy(p, sr.leftover); n > 0 {
		sr.leftover = sr.leftover[n:]
		return n, nil
	}
	sr.timer.Reset(streamStallTimeout)
	select {
	case chunk := <-sr.results:
		if chunk.err != nil {
			// The producer is done; let it drain and exit.
			sr.once.Do(func() { close(sr.stop) })
			return 0, chunk.err
		}
		n := copy(p, chunk.data)
		if n < len(chunk.data) {
			sr.leftover = chunk.data[n:]
		}
		return n, nil
	case <-sr.timer.C:
		// Producer send racing the timer: a chunk may be waiting right now.
		select {
		case chunk := <-sr.results:
			if chunk.err != nil {
				return 0, chunk.err
			}
			n := copy(p, chunk.data)
			if n < len(chunk.data) {
				sr.leftover = chunk.data[n:]
			}
			return n, nil
		default:
		}
		sr.dead.Store(true)
		sr.once.Do(func() { close(sr.stop) })
		return 0, fmt.Errorf("stream stalled: no data for %s", streamStallTimeout)
	}
}
