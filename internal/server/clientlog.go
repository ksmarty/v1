package server

import "sync"

// clientLogEntry is one browser-side debug record: an uncaught error (with its
// stack), a React render failure, or a failed API call with the request that
// failed.
type clientLogEntry struct {
	At    string         `json:"at"`
	Kind  string         `json:"kind"`
	Msg   string         `json:"msg"`
	Stack string         `json:"stack,omitempty"`
	Extra map[string]any `json:"extra,omitempty"`
}

// clientLogBucket holds the newest records reported by one user's browser, plus
// the browser's own description of itself (user agent, URL, viewport).
type clientLogBucket struct {
	client  map[string]any
	entries []clientLogEntry
}

// clientLogMax bounds what one browser can accumulate. The dump is read by a
// human, so the ring holds the newest records and no more.
const clientLogMax = 200

// clientLogs is a bounded, in-memory ring of browser debug records. It is
// deliberately not persisted: the records describe a browser session, and the
// server cannot tell when that session ended. They only need to survive until
// the next diagnostics export.
type clientLogs struct {
	mu   sync.Mutex
	byID map[string]*clientLogBucket
}

func newClientLogs() *clientLogs {
	return &clientLogs{byID: map[string]*clientLogBucket{}}
}

// add appends one batch, keeping only the newest clientLogMax entries. A batch
// that carries no client description keeps the one already known, so a later
// error-only report does not erase it.
func (c *clientLogs) add(userID string, client map[string]any, entries []clientLogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.byID[userID]
	if b == nil {
		b = &clientLogBucket{}
		c.byID[userID] = b
	}
	if len(client) > 0 {
		b.client = client
	}
	if len(entries) == 0 {
		return
	}
	b.entries = append(b.entries, entries...)
	if len(b.entries) > clientLogMax {
		b.entries = b.entries[len(b.entries)-clientLogMax:]
	}
}

// get returns a copy of the client description and the records reported so far,
// oldest first.
func (c *clientLogs) get(userID string) (map[string]any, []clientLogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.byID[userID]
	if b == nil {
		return nil, nil
	}
	out := make([]clientLogEntry, len(b.entries))
	copy(out, b.entries)
	return b.client, out
}
