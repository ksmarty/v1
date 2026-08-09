package server

import "sync"

// turnQueue holds messages sent while a run is active: the agent drains them
// between rounds (steer) and whatever remains becomes follow-up turns
// (queue).
type turnQueue struct {
	mu      sync.Mutex
	pending []string
}

func (q *turnQueue) add(msg string) {
	q.mu.Lock()
	q.pending = append(q.pending, msg)
	q.mu.Unlock()
}

// drain returns and clears all pending messages, oldest first.
func (q *turnQueue) drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	out := q.pending
	q.pending = nil
	return out
}

// turnManager tracks at most one active run per project.
type turnManager struct {
	mu   sync.Mutex
	runs map[string]*turnQueue
}

func newTurnManager() *turnManager {
	return &turnManager{runs: map[string]*turnQueue{}}
}

// beginOrQueue registers a message for the project atomically: if a run is
// active the message is queued onto it (started=false) and can never be lost
// to a run that just ended; otherwise a new run is registered for the caller
// to execute (started=true — msg is NOT queued, the caller runs it).
func (m *turnManager) beginOrQueue(projectID, msg string) (q *turnQueue, started bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q, ok := m.runs[projectID]; ok {
		if msg != "" {
			q.add(msg)
		}
		return q, false
	}
	q = &turnQueue{}
	m.runs[projectID] = q
	return q, true
}

// get returns the active run's queue, or nil when the project is idle.
func (m *turnManager) get(projectID string) *turnQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[projectID]
}

// end removes the run; the caller must drain follow-ups before calling it.
func (m *turnManager) end(projectID string) {
	m.mu.Lock()
	delete(m.runs, projectID)
	m.mu.Unlock()
}
