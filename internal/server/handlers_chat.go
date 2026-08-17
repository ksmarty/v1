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
	"v1/internal/llm"
	"v1/internal/screenshot"
	"v1/internal/store"
)

// compactTimeout bounds one compaction summarization call.
const compactTimeout = 10 * time.Minute

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

// chatSessionID resolves the request's sessionId, falling back to the
// project's default chat session (creating it when the project has none).
func (s *Server) chatSessionID(p *store.Project, sessionID string) string {
	if sessionID != "" {
		return sessionID
	}
	cs, err := s.st.EnsureDefaultSession(p.ID)
	if err != nil {
		return ""
	}
	return cs.ID
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		writeError(w, http.StatusInternalServerError, "failed to resolve chat session")
		return
	}
	msgs, err := s.st.ListMessages(p.ID, sessionID)
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
	userID := s.currentUser(r).ID
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	ctx, cancel := context.WithTimeout(r.Context(), compactTimeout)
	defer cancel()
	id, err := agent.CompactProject(ctx, s.st, p.ID, sessionID, s.llmClient(userID))
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
	userID := s.currentUser(r).ID
	var body struct {
		Message       string             `json:"message"`
		SessionID     string             `json:"sessionId"`
		Model         string             `json:"model"`
		ProviderID    string             `json:"providerId"`
		EditMessageID int64              `json:"editMessageId"`
		Thinking      string             `json:"thinking"`
		Attachments   []agent.Attachment `json:"attachments"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	// /plan <instruction> runs this turn in plan mode: the agent investigates
	// with read-only tools and produces a plan, changing nothing. The prefix
	// stays in the transcript so retries re-enter plan mode too.
	plan, isPlan := planCommand(body.Message)
	if isPlan && plan == "" {
		writeError(w, http.StatusBadRequest, "tell the agent what to plan, e.g. /plan add authentication")
		return
	}
	if msg := validateAttachments(body.Attachments); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if body.ProviderID != "" {
		prov := s.findLLMProvider(userID, body.ProviderID)
		if prov == nil {
			writeError(w, http.StatusBadRequest, "unknown_provider")
			return
		}
		if prov.APIKey == "" {
			writeError(w, http.StatusBadRequest, "no_api_key")
			return
		}
	} else if _, apiKey, _ := s.llmConfig(userID); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}

	params := agent.ChatParams{
		Store:           s.st,
		Project:         p,
		Client:          s.llmClientFor(userID, body.ProviderID),
		Message:         body.Message,
		SessionID:       s.chatSessionID(p, body.SessionID),
		Attachments:     body.Attachments,
		Model:           body.Model,
		Vision:          s.modelSupportsImages(userID, body.ProviderID, body.Model),
		ReasoningEffort: body.Thinking,
		PlanMode:        isPlan,
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
	if body.EditMessageID > 0 && s.turns.get(p.ID, params.SessionID) != nil {
		writeError(w, http.StatusConflict, "run_active")
		return
	}
	q, started, queuedID := s.turns.beginOrQueue(p.ID, params.SessionID, body.Message)
	if !started {
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "id": queuedID})
		return
	}
	s.streamChatTurn(w, r, p, userID, params, q)
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
		ID        int64  `json:"id"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID < 0 {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.DeleteMessagesAfter(p.ID, s.chatSessionID(p, body.SessionID), body.ID); err != nil {
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
	userID := s.currentUser(r).ID
	if _, apiKey, _ := s.llmConfig(userID); apiKey == "" {
		writeError(w, http.StatusBadRequest, "no_api_key")
		return
	}
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	last, err := s.st.LastUserMessage(p.ID, sessionID)
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
	_, isPlan := planCommand(last.Content)
	params := agent.ChatParams{
		Store:        s.st,
		Project:      p,
		Client:       s.llmClient(userID),
		Message:      last.Content,
		SessionID:    sessionID,
		Attachments:  agent.ParseAttachments(last.Attachments),
		Model:        last.Model,
		LastUserID:   last.ID,
		Vision:       s.modelSupportsImages(userID, "", last.Model),
		SkipSnapshot: true,
		PlanMode:     isPlan,
	}
	// A turn that failed mid-response (ran out of tokens, network error)
	// leaves a partial assistant message followed by the error row. Retrying
	// then continues from the partial instead of regenerating from scratch;
	// on success the partial and the continuation are folded into one message
	// and the error is dropped.
	if partialID := s.continuedPartial(p.ID, sessionID, last.ID); partialID > 0 {
		params.Message = ""
		params.LastUserID = 0
		params.ContinueFromID = partialID
	}
	q, started, _ := s.turns.beginOrQueue(p.ID, sessionID, "")
	if !started {
		writeError(w, http.StatusConflict, "run_active")
		return
	}
	s.streamChatTurn(w, r, p, userID, params, q)
}

// continuedPartial returns the id of the partial assistant reply to continue
// from when the conversation ends with [user, partial assistant, error] — the
// signature of a mid-response failure — and 0 otherwise.
func (s *Server) continuedPartial(projectID, sessionID string, userID int64) int64 {
	msgs, err := s.st.ListMessages(projectID, sessionID)
	if err != nil || len(msgs) < 3 {
		return 0
	}
	lastMsg := msgs[len(msgs)-1]
	prev := msgs[len(msgs)-2]
	if lastMsg.Role != "error" || prev.Role != "assistant" || prev.ID <= userID {
		return 0
	}
	if prev.Content == "" && prev.Reasoning == "" {
		return 0
	}
	return prev.ID
}

// planCommand extracts a plan-mode instruction from a message: "  /plan do X "
// → ("do X", true). "/planner" and friends don't match.
func planCommand(msg string) (string, bool) {
	trimmed := strings.TrimSpace(msg)
	rest, ok := strings.CutPrefix(trimmed, "/plan")
	if !ok {
		return "", false
	}
	if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// streamChatTurn runs one chat turn and streams it as SSE: reasoning and text
// deltas, tool events, then a done event carrying usage. Messages queued onto
// the run while it executes become follow-up turns on the same stream.
func (s *Server) streamChatTurn(w http.ResponseWriter, r *http.Request, p *store.Project, userID string, params agent.ChatParams, q *turnQueue) {
	// The run's events also fan out to watch listeners (clients that returned
	// to a running chat), so they see the stream live instead of waiting for
	// the snapshot to change.
	hub := s.turns.hub(p.ID, params.SessionID)
	defer s.turns.end(p.ID, params.SessionID)
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
		if hub != nil {
			hub.publish(ev)
		}
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	// The run is intentionally detached from the client connection: leaving
	// mid-generation (navigating away, backgrounding the app, closing the
	// tab) must not kill it, and it has no timeout — long generations run to
	// completion. The transcript is persisted either way; SSE writes to a
	// gone client simply fail and are ignored. The cancel function is
	// registered so the stop endpoint can still abort it explicitly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.turns.register(p.ID, params.SessionID, cancel)

	root, err := filepath.Abs(p.Path)
	if err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	// A new turn supersedes any question left pending by an earlier, dead run.
	if err := s.st.ClearPendingAsk(p.ID, params.SessionID); err != nil {
		emit(agent.ChatEvent{Type: "error", Error: err.Error()})
		return
	}
	// Sync MCP servers (cheap when already connected) and load enabled skills
	// into the system prompt before the turn starts.
	mcpTools, _ := s.mcp.Sync(ctx)
	params.ExtraTools = mcpTools
	params.SkillsPrompt = s.skillsSystemPrompt()
	params.GlobalPrompt = s.globalSystemPrompt(userID)
	params.ToonEnabled = s.toonEnabled(userID)
	params.RTKEnabled = s.rtkEnabled(userID)
	if mems, err := s.st.ListMemories(p.ID); err == nil {
		params.MemoriesPrompt = memoryPrompt(mems)
	}
	params.ContextBudget = s.cfg.ContextBudget
	params.ContextThreshold = s.contextThreshold(userID)
	params.Emit = emit
	params.Steer = q.steerDrain
	// Background commands are detached from the turn: the result is persisted
	// as a user message when the command finishes (so nothing is lost if the
	// turn ends first) and the running turn injects it at the next round.
	params.Background = s.background
	params.BackgroundNotify = func(job *agent.BackgroundJob) {
		text := agent.BackgroundResultText(job)
		msgID, err := s.st.AddMessage(p.ID, params.SessionID, "user", text, "", params.Client.Model, "", "", "")
		if err == nil {
			job.Text = text
			job.MsgID = msgID
		}
	}
	params.PollBackground = func() []agent.BackgroundResult {
		var out []agent.BackgroundResult
		for _, j := range s.background.Completed(params.SessionID) {
			out = append(out, agent.BackgroundResult{ID: j.ID, Text: j.Text, MessageID: j.MsgID})
		}
		return out
	}
	params.Exec = &agent.Executor{
		Root:           root,
		ProjectID:      p.ID,
		PreviewCommand: p.PreviewCommand,
		Previews:       s.previews,
		Store:          s.st,
		MCP:            s.mcp,
		Perm:           &turnPerm{s: s, emit: emit, userID: userID},
		OnTodos: func(t []store.Todo) {
			emit(agent.ChatEvent{Type: "todos", Todos: t})
		},
		OnMemories: func(mems []store.Memory) {
			emit(agent.ChatEvent{Type: "memories", Memories: mems})
		},
		OnFileChange: func() { s.previews.TouchRevision(p.ID) },
		OnProjectRename: func(name string) {
			emit(agent.ChatEvent{Type: "project_renamed", Text: name})
		},
		OnAsk: s.turnAsk(p.ID, params.SessionID, emit),
		RenderPage: func(ctx context.Context, url string) (string, error) {
			return screenshot.RenderText(ctx, url)
		},
	}
	if params.Vision {
		params.Exec.Screenshot = func(ctx context.Context, path string) ([]byte, error) {
			return s.capturePreview(ctx, p, path)
		}
	}
	for {
		turn, err := agent.RunChat(ctx, params)
		if err != nil {
			// Persist the failure so anyone returning to the chat sees what
			// happened (the SSE stream may be long gone by then). Explicit
			// cancellations (stop button, shutdown) are not errors worth
			// keeping.
			if !errors.Is(err, context.Canceled) {
				_, _ = s.st.AddMessage(p.ID, params.SessionID, "error", err.Error(), "", params.Client.Model, "", "", "")
			}
			emit(agent.ChatEvent{Type: "error", Error: err.Error()})
			return
		}
		// A continued turn (resumed from a mid-response failure) is folded
		// back into the partial it continued from: the continuation and the
		// error row disappear, and the partial becomes the full reply.
		if params.ContinueFromID > 0 {
			if mergedID, err := s.st.MergeContinuedTurn(p.ID, params.SessionID, params.ContinueFromID); err == nil {
				params.ContinueFromID = 0
				params.LastUserID = mergedID
				params.SkipSnapshot = true
			}
		}
		// Snapshot each finished turn as a git commit when the project is
		// repo-backed, so time-travel always has a checkpoint to return to.
		if !params.SkipSnapshot {
			_, _ = gitops.CommitIfRepo(root, params.Message, "")
		}
		emit(agent.ChatEvent{Type: "done", Usage: turn.Usage})

		// Messages queued during the run become follow-up turns in order, one
		// per message; anything left drains next iteration. Unconsumed steers
		// come first. A message being edited (held) is skipped by the drain;
		// the run waits for the edit to finish instead of ending.
		msgs := q.drain()
		if len(msgs) == 0 {
			if q.heldCount() > 0 {
				waiter := time.NewTicker(250 * time.Millisecond)
				defer waiter.Stop()
				for q.heldCount() > 0 {
					select {
					case <-ctx.Done():
						return
					case <-waiter.C:
					}
				}
				continue
			}
			return
		}
		for _, extra := range msgs[1:] {
			q.add(extra.Text)
		}
		m := msgs[0]
		msgID, err := s.st.AddMessage(p.ID, params.SessionID, "user", m.Text, "", params.Client.Model, "", "", "")
		if err != nil {
			emit(agent.ChatEvent{Type: "error", Error: err.Error()})
			return
		}
		emit(agent.ChatEvent{Type: "injected_message", MessageID: msgID, Text: m.Text})
		params.Message = m.Text
		params.Attachments = nil
		params.LastUserID = msgID
		params.SkipSnapshot = false
	}
}

// handleChatWatch attaches the caller to a running turn's live SSE stream:
// events from now on are relayed until the run finishes, so a client that
// returns to a chat mid-generation sees it exactly as if it had never left.
func (s *Server) handleChatWatch(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	hub := s.turns.hub(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId")))
	if hub == nil {
		// Nothing running — the stream simply ends.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch, release := hub.subscribe()
	defer release()
	for ev := range ch {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return
		}
		flusher.Flush()
	}
}

// handleChatStatus reports whether a run is active for the session.
func (s *Server) handleChatStatus(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": s.turns.running(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId"))),
	})
}

// handleChatStop aborts the project's active run, if any. Runs are detached
// from their client connection, so the client-side stream abort alone no
// longer stops them — the stop button calls this endpoint.
func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	_ = s.turns.cancelRun(p.ID, s.chatSessionID(p, r.URL.Query().Get("sessionId")))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleContextUsage reports the estimated context fill of the project's
// chat: tokens used vs the budget, plus the compaction threshold (the
// estimate mirrors the agent's own token estimation, excluding messages
// covered by a compaction snapshot).
func (s *Server) handleContextUsage(w http.ResponseWriter, r *http.Request) {
	p := s.projectOr404(w, r)
	if p == nil {
		return
	}
	sessionID := s.chatSessionID(p, r.URL.Query().Get("sessionId"))
	stored, err := s.st.ListMessages(p.ID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var coveredID int64
	if snap, err := s.st.GetCompactionSnapshot(p.ID, sessionID); err == nil {
		coveredID = snap.CoveredMessageID
	}
	msgs := make([]llm.Message, 0, len(stored))
	for _, m := range stored {
		if m.ID <= coveredID {
			continue
		}
		switch m.Role {
		case "user":
			msgs = append(msgs, llm.Message{Role: "user", Content: m.Content})
		case "assistant":
			msg := llm.Message{Role: "assistant", Content: m.Content, ReasoningContent: m.Reasoning}
			if m.ToolJSON != "" {
				var tj struct {
					ToolCalls []llm.ToolCall `json:"tool_calls"`
				}
				if json.Unmarshal([]byte(m.ToolJSON), &tj) == nil {
					msg.ToolCalls = tj.ToolCalls
				}
			}
			msgs = append(msgs, msg)
		case "tool":
			msg := llm.Message{Role: "tool", Content: m.Content}
			var tj struct {
				ToolCallID string `json:"tool_call_id"`
				Name       string `json:"name"`
			}
			if json.Unmarshal([]byte(m.ToolJSON), &tj) == nil {
				msg.ToolCallID = tj.ToolCallID
				msg.Name = tj.Name
			}
			msgs = append(msgs, msg)
		}
	}
	budget := s.cfg.ContextBudget
	if budget <= 0 {
		budget = 12000
	}
	userID := s.currentUser(r).ID
	// When the caller names a model, prefer its context window: the models.dev
	// catalog first, then the provider's own /models endpoint, then the fixed
	// server budget.
	if model := r.URL.Query().Get("model"); model != "" {
		baseURL, _, _ := s.llmConfig(userID)
		if pid := r.URL.Query().Get("providerId"); pid != "" {
			if p := s.findLLMProvider(userID, pid); p != nil {
				baseURL = p.BaseURL
			}
		}
		if n := s.catalogModelContext(baseURL, model); n > 0 {
			budget = n
		} else {
			_, apiKey, _ := s.llmConfig(userID)
			if pid := r.URL.Query().Get("providerId"); pid != "" {
				if p := s.findLLMProvider(userID, pid); p != nil {
					apiKey = p.APIKey
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			if n := llm.ModelContextLength(ctx, baseURL, apiKey, model); n > 0 {
				budget = n
			}
		}
	}
	threshold := s.contextThreshold(userID)
	writeJSON(w, http.StatusOK, map[string]any{
		"used":      agent.EstimateTokens(msgs),
		"budget":    budget,
		"threshold": int(float64(budget) * threshold),
	})
}

// catalogModelContext looks up a model's context window in the provider
// catalog: first among providers sharing the base URL, then any catalog entry
// with the same model id.
func (s *Server) catalogModelContext(baseURL, model string) int {
	cat := s.providerCatalog()
	if cat == nil {
		return 0
	}
	var fallback int
	for _, p := range cat.Providers {
		for _, m := range p.Models {
			if m.ID != model || m.Context <= 0 {
				continue
			}
			if p.BaseURL == baseURL {
				return m.Context
			}
			if fallback == 0 {
				fallback = m.Context
			}
		}
	}
	return fallback
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
func (s *Server) modelSupportsImages(userID, providerID, model string) bool {
	if model == "" {
		if p := s.findLLMProvider(userID, providerID); p != nil {
			model = p.Model
		} else {
			_, _, model = s.llmConfig(userID)
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
