package server

import (
	"log"
	"net/http"
	"time"

	"v1/internal/agent"
	"v1/internal/store"
)

const (
	// maxUploadContent bounds one uploaded file's encoded content (base64 for
	// images, the text itself for everything else). It is deliberately larger
	// than anything the model sees — the point of uploading early is that a big
	// file is already on the server before the user sends, not that it is small.
	maxUploadContent = 32 << 20
	// uploadTTL is how long an uploaded file waits for the message that will use
	// it. A chip the user deletes is removed at once; this only catches the
	// files left behind by a closed tab.
	uploadTTL = 24 * time.Hour
)

// handleUpload stores a file the composer attached, before the message that
// uses it exists. Uploading on attach is what makes a send cheap and reliable:
// the chat request then carries an id, so a large file cannot be lost halfway
// through a turn request, and the UI can show real progress while it moves.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Name    string `json:"name"`
		MIME    string `json:"mime"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadContent+(1<<20))
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(body.Content) > maxUploadContent {
		writeError(w, http.StatusRequestEntityTooLarge, "that file is too large to attach")
		return
	}
	if msg := validateAttachments([]agent.Attachment{{Name: body.Name, Kind: body.Kind}}); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	u := &store.Upload{
		ID:        store.NewID(),
		ProjectID: p.ID,
		SessionID: s.chatSessionID(p, r.URL.Query().Get("sessionId")),
		Name:      body.Name,
		MIME:      body.MIME,
		Kind:      body.Kind,
		Content:   body.Content,
		Size:      int64(len(body.Content)),
		CreatedAt: time.Now().Unix(),
	}
	if err := s.st.SaveUpload(u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Cheap housekeeping on the same path: a file attached and never sent must
	// not live in the database forever.
	if n, err := s.st.PruneUploads(time.Now().Add(-uploadTTL).Unix()); err == nil && n > 0 {
		log.Printf("uploads: pruned %d abandoned file(s)", n)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":   u.ID,
		"name": u.Name,
		"mime": u.MIME,
		"kind": u.Kind,
		"size": u.Size,
	})
}

// handleDeleteUploads drops uploaded files the user removed from the composer
// before sending, so an abandoned attachment does not wait out the TTL.
func (s *Server) handleDeleteUploads(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.st.DeleteUploads(p.ID, body.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
