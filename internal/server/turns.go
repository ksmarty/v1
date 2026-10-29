package server

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
)

// errNotQueued is returned when a reorder/steer references a message that is
// not in the run's queue.
var errNotQueued = errors.New("message is not in the queue")

// queuedMsg is one message waiting in a run's queue. Held messages are being
// edited in the UI: the follow-up drain skips them until they are released.
type queuedMsg struct {
	ID       string
	Text     string
	Held     bool
	QueuedAt time.Time
	// Attachments travel with the message: a queued or steered follow-up can
	// carry files exactly like a fresh turn can, so they must survive the queue
	// (including a reorder) and the drain that runs them.
	Attachments []agent.Attachment
}

// turnQueue holds messages sent while a run is active. They process in order
// as follow-up turns once the current turn finishes; a message can instead be
// steered explicitly, which injects it into the run at the next round
// boundary. The queue lives server-side, so it survives clients leaving.
type turnQueue struct {
	mu      sync.Mutex
	pending []queuedMsg // follow-ups, in processing order
	steers  []queuedMsg // explicitly steered, consumed between rounds
	// persist writes the queue through to the store after every change, so a
	// provider failure, a server restart or a long absence cannot drop a
	// message the user already sent. Nil when the queue is not backed by a
	// store (tests, and any caller that does not want durability).
	persist func(pending, steers []queuedMsg)
}

// persistLocked writes the queue through to the store. The caller must hold
// the lock: it snapshots the slices directly instead of re-locking.
func (q *turnQueue) persistLocked() {
	if q.persist == nil {
		return
	}
	pending := make([]queuedMsg, len(q.pending))
	copy(pending, q.pending)
	steers := make([]queuedMsg, len(q.steers))
	copy(steers, q.steers)
	q.persist(pending, steers)
}

// add appends a message to the queue and returns its id.
func (q *turnQueue) add(text string, atts []agent.Attachment) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := queuedMsg{ID: store.NewID(), Text: text, QueuedAt: time.Now(), Attachments: atts}
	q.pending = append(q.pending, m)
	q.persistLocked()
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

// remove deletes a pending (follow-up) message from the queue. Returns
// errNotQueued when the id is not a queued follow-up.
func (q *turnQueue) remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.pending {
		if q.pending[i].ID == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			q.persistLocked()
			return nil
		}
	}
	return errNotQueued
}

// reorder replaces the pending order with the given id list, which must be a
// permutation of the current ids.
func (q *turnQueue) reorder(ids []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(ids) != len(q.pending) {
		return errNotQueued
	}
	// Whole messages are carried over, not just their text: a reorder must not
	// drop a queued message's attachments.
	byID := make(map[string]queuedMsg, len(q.pending))
	for _, m := range q.pending {
		byID[m.ID] = m
	}
	next := make([]queuedMsg, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			return errNotQueued
		}
		delete(byID, id)
		next = append(next, m)
	}
	q.pending = next
	q.persistLocked()
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
			q.persistLocked()
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
			q.persistLocked()
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
			q.persistLocked()
			return nil
		}
	}
	return errNotQueued
}

// steerDrain returns and clears the steered messages, oldest first — the
// agent's mid-run steer hook.
func (q *turnQueue) steerDrain() []agent.SteerMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.steers) == 0 {
		return nil
	}
	out := make([]agent.SteerMessage, 0, len(q.steers))
	for _, m := range q.steers {
		out = append(out, agent.SteerMessage{Text: m.Text, Attachments: m.Attachments})
	}
	q.steers = nil
	q.persistLocked()
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
	q.persistLocked()
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
	mu        sync.Mutex
	subs      map[chan agent.ChatEvent]struct{}
	closed    bool
	history   []agent.ChatEvent // ring buffer of recent events for replay
	histHead  int
	histSize  int
	text      string // accumulated reply text so far (never reset on tool calls)
	reasoning string // accumulated thinking text so far
}

func newRunHub() *runHub {
	return &runHub{subs: map[chan agent.ChatEvent]struct{}{}, histSize: 512}
}

// record accumulates the in-flight reply so the history replay can seed a
// reconnecting client with everything produced so far. A new round (tool
// call) does NOT clear the buffer: the assistant message keeps its pre-tool
// content, and the model may keep appending after the tool result.
func (h *runHub) record(ev agent.ChatEvent) {
	switch ev.Type {
	case "delta":
		h.text += ev.Text
	case "reasoning":
		h.reasoning += ev.Text
	}
}

// subscribe returns a buffered channel that receives the run's events. The
// ring buffer of recent events is replayed first, in order, so an attaching
// client sees exactly what a client that stayed on the page saw: streamed
// deltas/reasoning, tool starts/ends, side effects, the final done — not a
// lossy text-only snapshot. Subsequent events are live. The channel closes
// when the run ends.
func (h *runHub) subscribe() (chan agent.ChatEvent, func()) {
	// Larger than the ring so the synchronous replay never drops events on a
	// normal-speed reader; slow consumers still degrade gracefully on live
	// events (publish uses non-blocking sends).
	ch := make(chan agent.ChatEvent, 1024)
	h.mu.Lock()
	if h.closed {
		close(ch)
	} else {
		if len(h.history) > 0 {
			start := 0
			if len(h.history) == h.histSize {
				start = h.histHead
			}
			for i := 0; i < len(h.history); i++ {
				ch <- h.history[(start+i)%len(h.history)]
			}
		}
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
	h.record(ev)
	if len(h.history) < h.histSize {
		h.history = append(h.history, ev)
	} else {
		h.history[h.histHead] = ev
		h.histHead = (h.histHead + 1) % h.histSize
	}
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

// runState is one active run: its queue, the event hub attached to it, and
// the user that started it (for the per-user concurrency cap).
type runState struct {
	queue  *turnQueue
	hub    *runHub
	userID string
}

// turnManager tracks at most one active run per chat session. Runs are
// detached from their client connection, so each one also carries a cancel
// function for explicit stops (the stop endpoint).
type turnManager struct {
	mu       sync.Mutex
	st       *store.Store // nil in tests: queues then live in memory only
	runs     map[string]*runState
	cancels  map[string]context.CancelFunc
	userRuns map[string]int // active concurrent runs per user
}

func newTurnManager(st *store.Store) *turnManager {
	return &turnManager{
		st:       st,
		runs:     map[string]*runState{},
		cancels:  map[string]context.CancelFunc{},
		userRuns: map[string]int{},
	}
}

// newQueue builds a queue bound to one session, with its store persistence
// hook installed, and hydrates whatever survived an earlier run.
func (m *turnManager) newQueue(projectID, sessionID string) *turnQueue {
	q := &turnQueue{}
	if m.st == nil {
		return q
	}
	q.persist = func(pending, steers []queuedMsg) {
		rows := make([]store.QueuedMessage, 0, len(pending)+len(steers))
		for i, msg := range pending {
			rows = append(rows, store.QueuedMessage{
				ID: msg.ID, Text: msg.Text, Held: msg.Held, Position: i,
				Attachments: agent.MarshalAttachments(msg.Attachments), QueuedAt: msg.QueuedAt.Unix(),
			})
		}
		for i, msg := range steers {
			rows = append(rows, store.QueuedMessage{
				ID: msg.ID, Text: msg.Text, Steer: true, Position: len(pending) + i,
				Attachments: agent.MarshalAttachments(msg.Attachments), QueuedAt: msg.QueuedAt.Unix(),
			})
		}
		if err := m.st.SaveQueuedMessages(projectID, sessionID, rows); err != nil {
			log.Printf("queue: saving %d message(s) for %s: %v", len(rows), runKey(projectID, sessionID), err)
		}
	}
	m.loadQueue(q, projectID, sessionID)
	return q
}

// loadQueue re-adds the persisted messages of a session to a fresh queue.
// This is what makes a queued message survive a provider failure or a restart:
// the next run in the session starts with them still waiting.
func (m *turnManager) loadQueue(q *turnQueue, projectID, sessionID string) {
	if m.st == nil {
		return
	}
	rows, err := m.st.ListQueuedMessages(projectID, sessionID)
	if err != nil {
		log.Printf("queue: loading %s: %v", runKey(projectID, sessionID), err)
		return
	}
	for _, r := range rows {
		msg := queuedMsg{
			ID: r.ID, Text: r.Text, Held: r.Held, QueuedAt: time.Unix(r.QueuedAt, 0),
			Attachments: agent.ParseAttachments(r.Attachments),
		}
		if r.Steer {
			q.steers = append(q.steers, msg)
		} else {
			q.pending = append(q.pending, msg)
		}
	}
}

// errTooManyRuns is returned by beginOrQueue when the per-user concurrent run
// cap is reached; the server maps it to HTTP 429.
var errTooManyRuns = errors.New("too many concurrent runs for this user")

// beginOrQueue registers a message for the session atomically: if a run is
// active the message is queued onto it (started=false, queuedID is its id)
// and can never be lost to a run that just ended; otherwise a new run is
// registered for the caller to execute (started=true — msg is NOT queued,
// the caller runs it). Starting a new run for userID is refused with
// errTooManyRuns when the user already has maxRuns active runs (maxRuns <= 0
// disables the cap) so one user can't saturate the host.
func (m *turnManager) beginOrQueue(projectID, sessionID, msg string, atts []agent.Attachment, userID string, maxRuns int) (q *turnQueue, started bool, queuedID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := runKey(projectID, sessionID)
	if rs, ok := m.runs[key]; ok {
		// A message with no text but with attachments is a complete turn of its
		// own (a screenshot, a log file), so it queues like any other.
		if msg != "" || len(atts) > 0 {
			queuedID = rs.queue.add(msg, atts)
		}
		return rs.queue, false, queuedID, nil
	}
	if maxRuns > 0 && userID != "" && m.userRuns[userID] >= maxRuns {
		return nil, false, "", errTooManyRuns
	}
	rs := &runState{queue: m.newQueue(projectID, sessionID), hub: newRunHub(), userID: userID}
	m.runs[key] = rs
	if userID != "" {
		m.userRuns[userID]++
	}
	return rs.queue, true, "", nil
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

// queueFor returns the session's queue: the live run's when one is active,
// otherwise a detached queue hydrated from the store. Queue endpoints use it
// so messages left over from a failed run can still be listed, edited,
// reordered or deleted — and so those edits persist.
func (m *turnManager) queueFor(projectID, sessionID string) *turnQueue {
	if q := m.get(projectID, sessionID); q != nil {
		return q
	}
	if m.st == nil {
		return nil
	}
	if rows, err := m.st.ListQueuedMessages(projectID, sessionID); err != nil || len(rows) == 0 {
		return nil
	}
	return m.newQueue(projectID, sessionID)
}

// takeQueued removes one queued message by id, from the live run's queue and
// from the store, and reports whether it was still there. A client re-sending
// a stranded queued message passes its id, which makes the send idempotent:
// the second attempt finds nothing and is reported as already queued.
func (m *turnManager) takeQueued(projectID, sessionID, id string) bool {
	removed := false
	if q := m.get(projectID, sessionID); q != nil {
		if q.remove(id) == nil {
			removed = true
		}
	}
	if m.st != nil {
		ok, err := m.st.DeleteQueuedMessage(projectID, sessionID, id)
		if err != nil {
			log.Printf("queue: deleting %s: %v", id, err)
		}
		if ok {
			removed = true
		}
	}
	return removed
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

// activeRun identifies a turn that is currently running.
type activeRun struct {
	ProjectID string
	SessionID string
}

// activeRuns reports the running turns as project/session pairs, so the
// dashboard can mark the session that is actually generating rather than the
// whole project. A project with several sessions needs the finer answer.
func (m *turnManager) activeRuns() []activeRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]activeRun, 0, len(m.runs))
	for key := range m.runs {
		if i := strings.IndexByte(key, '/'); i > 0 {
			out = append(out, activeRun{ProjectID: key[:i], SessionID: key[i+1:]})
		}
	}
	return out
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
	defer m.mu.Unlock()
	key := runKey(projectID, sessionID)
	if rs, ok := m.runs[key]; ok && rs.userID != "" {
		if n := m.userRuns[rs.userID]; n > 1 {
			m.userRuns[rs.userID] = n - 1
		} else {
			delete(m.userRuns, rs.userID)
		}
	}
	delete(m.runs, key)
	delete(m.cancels, key)
}
