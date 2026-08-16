package server

import (
	"context"
	"errors"
	"sync"

	"v1/internal/agent"
	"v1/internal/store"
)

// errNotQueued is returned when a reorder/steer references a message that is
// not in the run's queue.
var errNotQueued = errors.New("message is not in the queue")

// queuedMsg is one message waiting in a run's queue. Held messages are being
// edited in the UI: the follow-up drain skips them until they are released.
type queuedMsg struct {
	ID   string
	Text string
	Held bool
}

// turnQueue holds messages sent while a run is active. They process in order
// as follow-up turns once the current turn finishes; a message can instead be
// steered explicitly, which injects it into the run at the next round
// boundary. The queue lives server-side, so it survives clients leaving.
type turnQueue struct {
	mu      sync.Mutex
	pending []queuedMsg // follow-ups, in processing order
	steers  []queuedMsg // explicitly steered, consumed between rounds
}

// add appends a message to the queue and returns its id.
func (q *turnQueue) add(text string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := queuedMsg{ID: store.NewID(), Text: text}
	q.pending = append(q.pending, m)
	return m.ID
}

// list returns the pending messages in processing order.
func (q *turnQueue) list() []queuedMsg {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queuedMsg, len(q.pending))
	copy(out, q.pending)
	return out
}

// listSteers returns the messages waiting to be injected into the run.
func (q *turnQueue) listSteers() []queuedMsg {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queuedMsg, len(q.steers))
	copy(out, q.steers)
	return out
}

// reorder replaces the pending order with the given id list, which must be a
// permutation of the current ids.
func (q *turnQueue) reorder(ids []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(ids) != len(q.pending) {
		return errNotQueued
	}
	byID := make(map[string]string, len(q.pending))
	for _, m := range q.pending {
		byID[m.ID] = m.Text
	}
	next := make([]queuedMsg, 0, len(ids))
	for _, id := range ids {
		text, ok := byID[id]
		if !ok {
			return errNotQueued
		}
		delete(byID, id)
		next = append(next, queuedMsg{ID: id, Text: text})
	}
	q.pending = next
	return nil
}

// edit replaces a pending message's text, keeping its position, and releases
// any hold (the edit is done — the message can be sent again).
func (q *turnQueue) edit(id, text string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.pending {
		if q.pending[i].ID == id {
			q.pending[i].Text = text
			q.pending[i].Held = false
			return nil
		}
	}
	return errNotQueued
}

// hold marks a pending message as being edited (held=true) or released
// (held=false). Held messages are skipped by drain() until released.
func (q *turnQueue) hold(id string, held bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.pending {
		if q.pending[i].ID == id {
			q.pending[i].Held = held
			return nil
		}
	}
	return errNotQueued
}

// heldCount reports how many pending messages are currently being edited.
func (q *turnQueue) heldCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, m := range q.pending {
		if m.Held {
			n++
		}
	}
	return n
}

// steer moves one pending message onto the steer list: it is injected into
// the run at the next round boundary instead of waiting for the queue.
func (q *turnQueue) steer(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, m := range q.pending {
		if m.ID == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			q.steers = append(q.steers, m)
			return nil
		}
	}
	return errNotQueued
}

// steerDrain returns and clears the steered messages, oldest first — the
// agent's mid-run steer hook.
func (q *turnQueue) steerDrain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.steers) == 0 {
		return nil
	}
	out := make([]string, 0, len(q.steers))
	for _, m := range q.steers {
		out = append(out, m.Text)
	}
	q.steers = nil
	return out
}

// drain returns everything the run left behind — unconsumed steers first,
// then the pending queue in order (held messages stay put) — and clears the
// rest. The caller runs them as follow-up turns.
func (q *turnQueue) drain() []queuedMsg {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 && len(q.steers) == 0 {
		return nil
	}
	out := make([]queuedMsg, 0, len(q.steers)+len(q.pending))
	out = append(out, q.steers...)
	kept := q.pending[:0]
	for _, m := range q.pending {
		if m.Held {
			kept = append(kept, m)
		} else {
			out = append(out, m)
		}
	}
	q.pending = kept
	q.steers = nil
	if len(out) == 0 {
		return nil
	}
	return out
}

// runKey scopes runs per chat session: each session of a project can run its
// own turn independently.
func runKey(projectID, sessionID string) string {
	return projectID + "/" + sessionID
}

// runHub fans the run's SSE events out to extra listeners — clients that
// return to a chat while a generation is running and attach to the live
// stream (GET /chat/watch) instead of waiting for the snapshot to update.
type runHub struct {
	mu     sync.Mutex
	subs   map[chan agent.ChatEvent]struct{}
	closed bool
}

func newRunHub() *runHub {
	return &runHub{subs: map[chan agent.ChatEvent]struct{}{}}
}

// subscribe returns a buffered channel that receives the run's events from
// now on, and a release func. Slow listeners drop events rather than block
// the run.
func (h *runHub) subscribe() (chan agent.ChatEvent, func()) {
	ch := make(chan agent.ChatEvent, 128)
	h.mu.Lock()
	if h.closed {
		close(ch)
	} else {
		h.subs[ch] = struct{}{}
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

func (h *runHub) publish(ev agent.ChatEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// close ends the stream for every listener (the watch handlers return).
func (h *runHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		close(ch)
	}
	h.subs = map[chan agent.ChatEvent]struct{}{}
}

// runState is one active run: its queue and the event hub attached to it.
type runState struct {
	queue *turnQueue
	hub   *runHub
}

// turnManager tracks at most one active run per chat session. Runs are
// detached from their client connection, so each one also carries a cancel
// function for explicit stops (the stop endpoint).
type turnManager struct {
	mu      sync.Mutex
	runs    map[string]*runState
	cancels map[string]context.CancelFunc
}

func newTurnManager() *turnManager {
	return &turnManager{runs: map[string]*runState{}, cancels: map[string]context.CancelFunc{}}
}

// beginOrQueue registers a message for the session atomically: if a run is
// active the message is queued onto it (started=false, queuedID is its id)
// and can never be lost to a run that just ended; otherwise a new run is
// registered for the caller to execute (started=true — msg is NOT queued,
// the caller runs it).
func (m *turnManager) beginOrQueue(projectID, sessionID, msg string) (q *turnQueue, started bool, queuedID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := runKey(projectID, sessionID)
	if rs, ok := m.runs[key]; ok {
		if msg != "" {
			queuedID = rs.queue.add(msg)
		}
		return rs.queue, false, queuedID
	}
	rs := &runState{queue: &turnQueue{}, hub: newRunHub()}
	m.runs[key] = rs
	return rs.queue, true, ""
}

// get returns the active run's queue, or nil when the session is idle.
func (m *turnManager) get(projectID, sessionID string) *turnQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs, ok := m.runs[runKey(projectID, sessionID)]; ok {
		return rs.queue
	}
	return nil
}

// hub returns the active run's event hub for the session, or nil when idle.
func (m *turnManager) hub(projectID, sessionID string) *runHub {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs, ok := m.runs[runKey(projectID, sessionID)]; ok {
		return rs.hub
	}
	return nil
}

// running reports whether a run is active for the session — the client polls
// this while the app is open so it can attach to the live stream or refresh
// when the run finishes.
func (m *turnManager) running(projectID, sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.runs[runKey(projectID, sessionID)]
	return ok
}

// register attaches the run's cancel function (called from the stop
// endpoint); the caller must have won beginOrQueue first.
func (m *turnManager) register(projectID, sessionID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.cancels[runKey(projectID, sessionID)] = cancel
	m.mu.Unlock()
}

// cancelRun cancels the session's active run, if any, and reports whether one
// was running.
func (m *turnManager) cancelRun(projectID, sessionID string) bool {
	m.mu.Lock()
	cancel, ok := m.cancels[runKey(projectID, sessionID)]
	m.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	return ok
}

// end removes the run; the caller must drain follow-ups before calling it.
func (m *turnManager) end(projectID, sessionID string) {
	m.mu.Lock()
	key := runKey(projectID, sessionID)
	delete(m.runs, key)
	delete(m.cancels, key)
	m.mu.Unlock()
}
