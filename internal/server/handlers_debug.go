package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"v1/internal/sanitize"
)

// maxClientLogBody caps what one browser may report in a single batch. The
// client sends an error together with its recent breadcrumbs, so this is far
// above a normal payload; it exists so a broken or hostile client cannot make
// the server allocate without bound.
const maxClientLogBody = 1 << 20

// handleClientLog accepts the browser's debug records — uncaught errors with
// their stacks, React render failures, and failed API calls — and keeps the
// newest ones for the chat diagnostics dump.
//
// The dump is composed entirely server-side, so a failure that happens only in
// the browser (a throw while loading or rendering a session, a request that
// never left the client) leaves no trace in it. Without this endpoint the export
// a user sends for a broken chat says nothing about what their browser did.
func (s *Server) handleClientLog(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	var body struct {
		Client  map[string]any   `json:"client"`
		Entries []clientLogEntry `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClientLogBody)).Decode(&body); err != nil {
		http.Error(w, "invalid client log payload", http.StatusBadRequest)
		return
	}
	if len(body.Entries) == 0 && len(body.Client) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.clientLogs.add(userID, body.Client, body.Entries)
	for _, e := range body.Entries {
		// Mirror real failures into the server log too: a user reporting a
		// broken chat usually has the container logs to hand, while the dump
		// needs an explicit export. Sanitized because the message is arbitrary
		// text from the browser and the log is a terminal.
		switch e.Kind {
		case "error", "unhandledrejection", "react", "fetch":
			log.Printf("client error [%s] %s", e.Kind, sanitize.Text(e.Msg))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
