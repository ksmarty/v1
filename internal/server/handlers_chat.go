package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"v1/internal/agent"
	"v1/internal/gitops"
	"v1/internal/screenshot"
	"v1/internal/store"
)

// chatTimeout bounds one full chat turn (all tool rounds). Reasoning streams
// can run long, so it is generous.
const chatTimeout = 15 * time.Minute

// Attachment size caps keep a single turn's request and stored message sane.
const (
	maxAttachmentsPerMsg = 6
	maxImageAttachment   = 3 << 20 // base64 string (≈2.2 MB decoded)
	maxTextAttachment    = 200 << 10
	maxTotalAttachments  = 8 << 20
)

// validateAttachments returns an error message for invalid attachment lists,
// or "" when the list is fine.
func validateAttachments(atts []agent.Attachment) string {
	if len(atts) > maxAttachmentsPerMsg {
		return fmt.Sprintf("too many attachments (max %d)", maxAttachmentsPerMsg)
	}
	total := 0
	for _, a := range atts {
		if a.Name == "" {
			return "attachment name is required"
		}
		switch a.Kind {
		case "image":
			if len(a.Content) > maxImageAttachment {
				return fmt.Sprintf("image attachment %q is too large (max ~2 MB)", a.Name)
			}
		case "text":
			if len(a.Content) > maxTextAttachment {
				return fmt.Sprintf("text attachment %q is too large (max 200 KB)", a.Name)
			}
		default:
			return fmt.Sprintf("unsupported attachment kind %q (use text or image)", a.Kind)
		}
		total += len(a.Content)
	}
	if total > maxTotalAttachments {
		return "attachments are too large in total (max ~6 MB)"
	}
	return ""
}

// ---- messages ----

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	msgs, err := s.st.ListMessages(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type usageJSON struct {
		Input  int64  `json:"input"`
		Output int64  `json:"output"`
		Model  string `json:"model"`
	}
	type attachmentMeta struct {
		Name string `json:"name"`
		MIME string `json:"mime"`
		Kind string `json:"kind"`
		Size int    `json:"size"`
	}
	type messageJSON struct {
		ID          int64            `json:"id"`
		Role        string           `json:"role"`
		Content     string           `json:"content"`
		Tool        json.RawMessage  `json:"tool,omitempty"`
		Model       string           `json:"model,omitempty"`
		Reasoning   string           `json:"reasoning,omitempty"`
		Usage       *usageJSON       `json:"usage,omitempty"`
		Attachments []attachmentMeta `json:"attachments,omitempty"`
		CreatedAt   int64            `json:"createdAt"`
	}
	out := []messageJSON{}
	for _, m := range msgs {
		mj := messageJSON{ID: m.ID, Role: m.Role, Content: m.Content, Model: m.Model, Reasoning: m.Reasoning, CreatedAt: m.CreatedAt}
		if m.ToolJSON != "" {
			mj.Tool = json.RawMessage(m.ToolJSON)
		}
		if m.Usage != "" {
			var u usageJSON
			if json.Unmarshal([]byte(m.Usage), &u) == nil {
				mj.Usage = &u
			}
		}
		if m.Attachments != "" {
			var atts []agent.Attachment
			if json.Unmarshal([]byte(m.Attachments), &atts) == nil {
				meta := make([]attachmentMeta, 0, len(atts))
				for _, a := range atts {
					meta = append(meta, attachmentMeta{Name: a.Name, MIME: a.MIME, Kind: a.Kind, Size: len(a.Content)})
				}
				mj.Attachments = meta
			}
		}
		out = append(out, mj)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMessageAttachment serves one stored attachment's raw content. Images
// are decoded from base64 and served with their MIME type so the chat UI can
// use the URL directly as an <img> source.
func (s *Server) handleMessageAttachment(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgId"), 10, 64)
	if err != nil || msgID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 {
		writeError(w, http.StatusBadRequest, "invalid attachment index")
		return
	}
	m, err := s.st.GetMessage(p.ID, msgID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	atts := agent.ParseAttachments(m.Attachments)
	if idx >= len(atts) {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	a := atts[idx]
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if a.Kind == "image" {
		data, err := base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid image data")
			return
		}
		w.Header().Set("Content-Type", a.MIME)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(a.Content))
}

// handleGetTodos returns the project's agent-maintained todo list.
func (s *Server) handleGetTodos(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	todos, err := s.st.GetTodos(p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"todos": todos})
}

// ---- chat (SSE agent loop) ----

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	id, err := agent.CompactProject(ctx, s.st, p.ID, s.llmClient())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"coveredMessageId": id})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		Message       string             `json:"message"`
		Model         string             `json:"model"`
		ProviderID    string             `json:"providerId"`
		EditMessageID int64              `json:"editMessageId"`
		Attachments   []agent.Attachment `json:"attachments"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if msg := validateAttachments(body.Attachments); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if body.ProviderID != "" {
		prov := s.findLLMProvider(body.ProviderID)
		if prov == nil {
			writeError(w, http.StatusBadRequest, "unknown_provider")
			return
		}
		if prov.APIKey == "" {
			writeError(w, http.StatusBadRequest, "no_api_key")
			return
		}
	} else if _, apiKey, _ := s.llmConfig(); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}

	params := agent.ChatParams{
		Store:       s.st,
		Project:     p,
		Client:      s.llmClientFor(body.ProviderID),
		Message:     body.Message,
		Attachments: body.Attachments,
		Model:       body.Model,
		Vision:      s.modelSupportsImages(body.ProviderID, body.Model),
	}
	// Editing an existing user message rewinds the thread to it and re-runs
	// from the edited text: update its content, then run with LastUserID set so
	// history is truncated at it and the message is not re-added.
	if body.EditMessageID > 0 {
		msg, err := s.st.GetMessage(p.ID, body.EditMessageID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if msg.Role != "user" {
			writeError(w, http.StatusBadRequest, "can only edit a user message")
			return
		}
		if err := s.st.UpdateMessageContent(p.ID, msg.ID, body.Message); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		params.LastUserID = msg.ID
		params.SkipSnapshot = true
	}

	// Mid-run sends steer (consumed next round) or queue (a follow-up turn).
	// Edits rewrite history, so they must wait for a quiet project.
	if body.EditMessageID > 0 && s.turns.get(p.ID) != nil {
		writeError(w, http.StatusConflict, "run_active")
		return
	}
	q, started := s.turns.beginOrQueue(p.ID, body.Message)
	if !started {
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
		return
	}
	s.streamChatTurn(w, r, p, params, q)
}

// handleTruncateMessages deletes every message after the given id — the
// "revert"/rewind action that cuts the thread back to a chosen point.
// id 0 deletes the whole thread (the chat's /clear command).
func (s *Server) handleTruncateMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID < 0 {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.DeleteMessagesAfter(p.ID, body.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChatRetry(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	if _, apiKey, _ := s.llmConfig(); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}
	last, err := s.st.LastUserMessage(p.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "no_user_turn")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Re-run the last user turn with its stored model, falling back to the
	// current client model when the stored message has none.
	q, started := s.turns.beginOrQueue(p.ID, "")
	if !started {
		writeError(w, http.StatusConflict, "run_active")
		return
	}
	s.streamChatTurn(w, r, p, agent.ChatParams{
		Store:        s.st,
		Project:      p,
		Client:       s.llmClient(),
		Message:      last.Content,
		Attachments:  agent.ParseAttachments(last.Attachments),
		Model:        last.Model,
		LastUserID:   last.ID,
		Vision:       s.modelSupportsImages("", last.Model),
		SkipSnapshot: true,
	}, q)
}

// streamChatTurn runs one chat turn and streams it as SSE: reasoning and text
// deltas, tool events, then a done event carrying usage. Messages queued onto
// the run while it executes become follow-up turns on the same stream.
func (s *Server) streamChatTurn(w http.ResponseWriter, r *http.Request, p *store.Project, params agent.ChatParams, q *turnQueue) {
	defer s.turns.end(p.ID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(ev agent.ChatEvent) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()

	root, err := filepath.Abs(p.Path)
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	// Sync MCP servers (cheap when already connected) and load enabled skills
	// into the system prompt before the turn starts.
	mcpTools, _ := s.mcp.Sync(ctx)
	params.ExtraTools = mcpTools
	params.SkillsPrompt = s.skillsSystemPrompt()
	if mems, err := s.st.ListMemories(p.ID); err == nil && len(mems) > 0 {
		var b strings.Builder
		b.WriteString("Project memories (facts you saved in earlier turns; use the forget tool with an id to delete one):")
		for _, m := range mems {
			fmt.Fprintf(&b, "\n- [%d] %s", m.ID, m.Content)
		}
		params.MemoriesPrompt = b.String()
	}
	params.ContextBudget = s.cfg.ContextBudget
	params.ContextThreshold = s.cfg.ContextThreshold
	params.Emit = emit
	params.Steer = q.drain
	params.Exec = &agent.Executor{
		Root:           root,
		ProjectID:      p.ID,
		PreviewCommand: p.PreviewCommand,
		Previews:       s.previews,
		Store:          s.st,
		MCP:            s.mcp,
		Perm:           &turnPerm{s: s, emit: emit},
		OnTodos: func(t []store.Todo) {
			emit(agent.ChatEvent{Type: "todos", Todos: t})
		},
		OnFileChange: func() { s.previews.TouchRevision(p.ID) },
		OnAsk:        s.turnAsk(emit),
	}
	if params.Vision {
		params.Exec.Screenshot = func(ctx context.Context, path string) ([]byte, error) {
			return s.capturePreview(ctx, p, path)
		}
	}
	for {
		turn, err := agent.RunChat(ctx, params)
		if err != nil {
			emit(agent.ChatEvent{Type: "error", Error: err.Error()})
			return
		}
		// Snapshot each finished turn as a git commit when the project is
		// repo-backed, so time-travel always has a checkpoint to return to.
		if !params.SkipSnapshot {
			_, _ = gitops.CommitIfRepo(root, params.Message, "")
		}
		emit(agent.ChatEvent{Type: "done", Usage: turn.Usage})

		// Messages queued during the run become follow-up turns, one per
		// message; anything left drains next iteration.
		msgs := q.drain()
		if len(msgs) == 0 {
			return
		}
		for _, extra := range msgs[1:] {
			q.add(extra)
		}
		m := msgs[0]
		msgID, err := s.st.AddMessage(p.ID, "user", m, "", params.Client.Model, "", "", "")
		if err != nil {
			emit(agent.ChatEvent{Type: "error", Error: err.Error()})
			return
		}
		emit(agent.ChatEvent{Type: "injected_message", MessageID: msgID, Text: m})
		params.Message = m
		params.Attachments = nil
		params.LastUserID = msgID
		params.SkipSnapshot = false
	}
}

// capturePreview ensures the project's preview is running and screenshots it.
// Node previews are reached directly on their dev-server port (the v1 proxy
// requires a session when auth is on); static previews go through the proxy,
// which needs auth disabled.
func (s *Server) capturePreview(ctx context.Context, p *store.Project, path string) ([]byte, error) {
	rel, err := s.previews.Start(p.ID, p.Path, p.PreviewCommand)
	if err != nil {
		return nil, err
	}
	base := s.previews.DirectURL(p.ID)
	if base == "" {
		base = fmt.Sprintf("http://127.0.0.1:%d%s", s.cfg.Port, rel)
	}
	if path != "" && path != "/" {
		base = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
	}
	return screenshot.Capture(ctx, base)
}

// modelSupportsImages reports whether the given model carries image-input
// metadata in the provider catalog — gates the screenshot_app tool.
func (s *Server) modelSupportsImages(providerID, model string) bool {
	if model == "" {
		if p := s.findLLMProvider(providerID); p != nil {
			model = p.Model
		} else {
			_, _, model = s.llmConfig()
		}
	}
	cat := s.providerCatalog()
	if cat == nil {
		return false
	}
	for _, prov := range cat.Providers {
		if providerID != "" && prov.ID != providerID {
			continue
		}
		for _, m := range prov.Models {
			if m.ID == model && m.ImageInput {
				return true
			}
		}
	}
	return false
}

// ---- preview control ----

func (s *Server) handlePreviewStatus(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	running, url, logs := s.previews.Status(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"running":  running,
		"url":      url,
		"logs":     logs,
		"revision": s.previews.Revision(p.ID),
	})
}

func (s *Server) handlePreviewStart(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	url, err := s.previews.Start(p.ID, p.Path, p.PreviewCommand)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

func (s *Server) handlePreviewStop(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.previews.Stop(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- terminal ----

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	s.terminals.ServeWS(w, r, p.ID, p.Path)
}
