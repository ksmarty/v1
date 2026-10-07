package server

import (
	"net/http"
)

// Background jobs are the detached commands the agent starts with
// run_command_background. They outlive the turn that started them, so the UI
// needs to be able to see what is still running and stop one: a command that
// was started by mistake, or one that is simply never going to finish, would
// otherwise run to its timeout with no way out.
//
// Only running jobs are served. A finished job's output is already in the
// transcript as the "[Background #…] finished" row, which is what the UI
// reloads from — keeping a second copy here would let the two disagree.

// backgroundJobJSON is one running job as the UI sees it.
type backgroundJobJSON struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	StartedAt int64  `json:"startedAt"`
}

// handleListBackground returns the session's still-running background jobs,
// oldest first.
func (s *Server) handleListBackground(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	jobs := []backgroundJobJSON{}
	if s.background != nil {
		for _, j := range s.background.Running(sessionID) {
			jobs = append(jobs, backgroundJobJSON{
				ID:        j.ShortID(),
				Command:   j.Command,
				StartedAt: j.StartedAt.Unix(),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// handleCancelBackground stops one running background job of the session. The
// job's own completion path still runs: the transcript gets its result row
// marked "cancelled", so the agent learns the command did not finish instead of
// waiting for output that will never come.
func (s *Server) handleCancelBackground(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		SessionID string `json:"sessionId"`
		JobID     string `json:"jobId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.JobID == "" {
		writeError(w, http.StatusBadRequest, "jobId is required")
		return
	}
	if s.background == nil {
		writeError(w, http.StatusNotFound, "no background job with that id")
		return
	}
	job := s.background.Cancel(s.chatSessionID(p, body.SessionID), body.JobID)
	if job == nil {
		// Finished, or never existed: either way there is nothing to stop, and
		// the UI refreshes its list from the response.
		writeError(w, http.StatusNotFound, "no running background job with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": job.ShortID()})
}
