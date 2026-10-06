// Package harness — bridge.go is the Go client for the pi-durable sidecar: the
// typed RPC calls v1 makes (ensure / watch / submit), the event types the
// sidecar pushes back, and the routing of host-tool calls to the turn that
// asked for them.
//
// The transport (rpc.go) and the process lifecycle (supervisor.go) are
// deliberately separate: this file only knows the protocol.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Event is one pi-durable AgentEvent as forwarded by the sidecar. Only the
// fields v1 consumes are decoded; the union is wide and mostly irrelevant to
// the SSE translation.
type Event struct {
	Type string `json:"type"`
	// Changes carries the streamed deltas on "message_update".
	Changes []Change `json:"changes,omitempty"`
	// Usage is the running token/cost total on "message_update".
	Usage *Usage `json:"usage,omitempty"`
	// Tool fields on "tool_execution_start" / "tool_execution_end".
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	// Entry is present on a "tool_execution_end" whose tool task actually ran,
	// and absent when the task faulted or was orphaned. Only presence matters,
	// so it stays raw: entry ids are numbers and their shape is pi-durable's
	// business, not v1's.
	Entry json.RawMessage `json:"entry,omitempty"`
	// Attempt is the retry counter on "auto_retry_start".
	Attempt int `json:"attempt,omitempty"`
	// Message is whatever the event type puts under "message": a message
	// object on "message_start", a plain string on "task_failed".
	Message json.RawMessage `json:"message,omitempty"`
	// Compaction fields on "compaction_start" / "compaction_end": "manual",
	// "threshold" or "overflow", and whether the compaction blocks the turn.
	Reason   string `json:"reason,omitempty"`
	Blocking bool   `json:"blocking,omitempty"`
}

// ErrorMessage is the failure text on "task_failed" (a faulted or orphaned
// generation: provider error, retries exhausted).
func (e *Event) ErrorMessage() string {
	if len(e.Message) == 0 {
		return "the generation failed"
	}
	var s string
	if err := json.Unmarshal(e.Message, &s); err == nil && s != "" {
		return s
	}
	return string(e.Message)
}

// Change is one MessageChange: a delta into the live assistant message.
type Change struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}

// Usage is pi-ai's accounting for one model call.
type Usage struct {
	Input       int64 `json:"input,omitempty"`
	Output      int64 `json:"output,omitempty"`
	CacheRead   int64 `json:"cacheRead,omitempty"`
	CacheWrite  int64 `json:"cacheWrite,omitempty"`
	TotalTokens int64 `json:"totalTokens,omitempty"`
	Cost        *Cost `json:"cost,omitempty"`
}

// Cost is pi-ai's cost breakdown; Total is what v1 displays.
type Cost struct {
	Total *float64 `json:"total,omitempty"`
}

// ToolCall is one host-tool invocation forwarded by the sidecar. The
// conversation id is how a call finds the turn that owns it, since one sidecar
// serves every session at once.
type ToolCall struct {
	ConversationID string
	Tool           string
	Args           json.RawMessage
	CallID         string
}

// ToolResult is what the sidecar turns into the model-visible tool result.
type ToolResult struct {
	Text    string          `json:"text"`
	IsError bool            `json:"isError"`
	Details json.RawMessage `json:"details,omitempty"`
	// Images are pictures a tool produced for the model to look at. Only
	// screenshot_app does this, and only for a vision model.
	Images []ToolImage `json:"images,omitempty"`
}

// ToolImage is one image attached to a tool result, base64 as the provider
// APIs want it.
type ToolImage struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// ToolRunner executes host tools and resolves approvals for one conversation.
// v1's agent.Executor and permission resolver implement it behind a thin
// adapter, so tool semantics stay in Go.
type ToolRunner interface {
	RunTool(ctx context.Context, call ToolCall) (ToolResult, error)
	Authorize(ctx context.Context, call ToolCall) (block string, err error)
}

// EnsureRequest registers (or reuses) the conversation behind one v1 chat
// session. An empty field means "leave unchanged".
type EnsureRequest struct {
	V1SessionID    string       `json:"v1SessionId,omitempty"`
	ConversationID string       `json:"conversationId,omitempty"`
	Cwd            string       `json:"cwd,omitempty"`
	Instructions   string       `json:"instructions,omitempty"`
	Provider       ProviderSpec `json:"provider"`
	Model          ModelRef     `json:"model"`
	ThinkingLevel  string       `json:"thinkingLevel,omitempty"`
	// ToolDefs are the model-facing tool definitions for this turn. v1 owns
	// them (agent.ChatParams.ToolSet) so the two harnesses can never advertise
	// different schemas; the sidecar only proxies calls back to Go.
	ToolDefs []ToolDef `json:"toolDefs,omitempty"`
}

// ToolDef is one tool as the model sees it: the name, the description, and the
// JSON Schema of its arguments, straight from v1's Go definitions.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ProviderSpec is v1's provider descriptor in pi-ai's shape.
type ProviderSpec struct {
	ID            string      `json:"id"`
	Name          string      `json:"name,omitempty"`
	BaseURL       string      `json:"baseUrl"`
	APIKey        string      `json:"apiKey"`
	API           string      `json:"api,omitempty"`
	SessionHeader string      `json:"sessionHeader,omitempty"`
	Models        []ModelSpec `json:"models,omitempty"`
}

// ModelSpec is one model of a provider, in pi-ai's shape.
type ModelSpec struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	Input         []string `json:"input,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
}

// ModelRef selects the model a conversation runs on.
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// EnsureResult is the sidecar's answer to Ensure.
type EnsureResult struct {
	ConversationID string `json:"conversationId"`
	ProviderID     string `json:"providerId"`
	ModelID        string `json:"modelId"`
}

// SubmitResult is the sidecar's answer to Submit.
type SubmitResult struct {
	ConversationID string `json:"conversationId"`
	SubmissionID   string `json:"submissionId"`
}

// Bridge is the typed client for one sidecar. It also owns the routing table
// for inbound host-tool calls and the event queues feeding each turn.
type Bridge struct {
	sup  *Supervisor
	logf func(format string, args ...any)

	mu      sync.Mutex
	runners map[string]ToolRunner
	streams map[string]*EventQueue
}

// NewBridge wires a bridge to a running supervisor and installs it as the
// supervisor's inbound handler.
func NewBridge(sup *Supervisor, logf func(string, ...any)) *Bridge {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	b := &Bridge{
		sup:     sup,
		logf:    logf,
		runners: map[string]ToolRunner{},
		streams: map[string]*EventQueue{},
	}
	sup.SetHandler(b.Handle)
	return b
}

// Handle answers the sidecar's inbound calls. It is the harness.Handler, so it
// runs for requests off the read loop and for notifications on it — event
// pushes must therefore never block (see EventQueue).
func (b *Bridge) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "event":
		var p struct {
			SubscriptionID string  `json:"subscriptionId"`
			ConversationID string  `json:"conversationId"`
			Events         []Event `json:"events"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &HandlerError{Code: CodeInvalidParams, Message: err.Error()}
		}
		b.mu.Lock()
		q := b.streams[p.SubscriptionID]
		b.mu.Unlock()
		if q == nil {
			// A late batch from a stopped subscription; dropping it is correct.
			return nil, nil
		}
		q.Push(p.Events)
		return nil, nil
	case "tool.call":
		call, err := decodeToolCall(params)
		if err != nil {
			return nil, err
		}
		runner := b.runner(call.ConversationID)
		if runner == nil {
			return ToolResult{Text: fmt.Sprintf("no active turn for tool %s", call.Tool), IsError: true}, nil
		}
		return runner.RunTool(ctx, call)
	case "tool.authorize":
		call, err := decodeToolCall(params)
		if err != nil {
			return nil, err
		}
		runner := b.runner(call.ConversationID)
		if runner == nil {
			return map[string]any{}, nil
		}
		block, err := runner.Authorize(ctx, call)
		if err != nil {
			// A denial is reported to the model as a blocked call, not as a
			// bridge failure: the sidecar records the intent durably.
			return map[string]any{"block": err.Error()}, nil
		}
		if block == "" {
			return map[string]any{}, nil
		}
		return map[string]any{"block": block}, nil
	default:
		return nil, &HandlerError{Code: CodeMethodNotFound, Message: "unsupported method " + method}
	}
}

func decodeToolCall(params json.RawMessage) (ToolCall, error) {
	var p struct {
		Tool           string          `json:"tool"`
		Arguments      json.RawMessage `json:"arguments"`
		CallID         string          `json:"callId"`
		ConversationID string          `json:"conversationId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ToolCall{}, &HandlerError{Code: CodeInvalidParams, Message: err.Error()}
	}
	return ToolCall{ConversationID: p.ConversationID, Tool: p.Tool, Args: p.Arguments, CallID: p.CallID}, nil
}

func (b *Bridge) runner(conversationID string) ToolRunner {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runners[conversationID]
}

// Register makes runner answer this conversation's tool calls until the
// returned function is called. It is called once per turn: the runner closes
// over the turn's executor and permission resolver.
func (b *Bridge) Register(conversationID string, runner ToolRunner) func() {
	b.mu.Lock()
	b.runners[conversationID] = runner
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.runners[conversationID] == runner {
			delete(b.runners, conversationID)
		}
		b.mu.Unlock()
	}
}

// Ensure registers or updates the conversation for one v1 chat session.
func (b *Bridge) Ensure(ctx context.Context, req EnsureRequest) (EnsureResult, error) {
	var out EnsureResult
	err := b.sup.Call(ctx, "conversation.ensure", req, &out)
	return out, err
}

// Watch starts a subscription for one conversation and returns the queue its
// events land on. The returned stop function ends the subscription.
func (b *Bridge) Watch(ctx context.Context, subscriptionID, conversationID string) (*EventQueue, func(), error) {
	q := NewEventQueue()
	b.mu.Lock()
	b.streams[subscriptionID] = q
	b.mu.Unlock()
	stop := func() {
		b.mu.Lock()
		if b.streams[subscriptionID] == q {
			delete(b.streams, subscriptionID)
		}
		b.mu.Unlock()
		q.Close(nil)
	}
	if err := b.sup.Call(ctx, "watch.start", map[string]any{"subscriptionId": subscriptionID, "conversationId": conversationID}, nil); err != nil {
		stop()
		return nil, nil, err
	}
	return q, stop, nil
}

// Submit queues one user turn on a conversation. whenBusy decides what happens
// if the sidecar is already running one: pi-durable's modes are "followUp",
// "steer" and "reject". v1 runs at most one turn per session, so a second
// submit means something is wrong and "reject" fails loudly instead of
// silently queueing.
// Submit queues one user turn on a conversation. content is either the message
// text or []InputPart when the turn carries attachments.
func (b *Bridge) Submit(ctx context.Context, conversationID, requestID string, content any, whenBusy string) (SubmitResult, error) {
	var out SubmitResult
	err := b.sup.Call(ctx, "turn.submit", map[string]any{
		"conversationId": conversationID,
		"requestId":      requestID,
		"content":        content,
		"whenBusy":       whenBusy,
	}, &out)
	return out, err
}

// InputPart is one piece of a user submission, in the shape pi-durable hands
// the model: a text block, or an image to look at.
type InputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Steer hands the sidecar a message to place after the current tool round,
// joining the running turn — the pi-durable equivalent of the built-in loop's
// mid-run injection.
func (b *Bridge) Steer(ctx context.Context, conversationID, requestID, text string) error {
	return b.sup.Call(ctx, "turn.steer", map[string]any{
		"conversationId": conversationID,
		"requestId":      requestID,
		"text":           text,
	}, nil)
}

// Abort withdraws queued inputs and stops the conversation's live work. The
// caller's own context is usually already cancelled by the time this runs, so
// pass a detached one.
func (b *Bridge) Abort(ctx context.Context, conversationID string) error {
	return b.sup.Call(ctx, "turn.abort", map[string]any{"conversationId": conversationID}, nil)
}

// Compact asks the sidecar to summarise the conversation. pi-durable places the
// summary through a write submission, so this returns once the task is queued
// rather than when the summary lands.
func (b *Bridge) Compact(ctx context.Context, conversationID string) error {
	return b.sup.Call(ctx, "turn.compact", map[string]any{"conversationId": conversationID}, nil)
}

// Shutdown asks the sidecar to close its store and exit.
func (b *Bridge) Shutdown(ctx context.Context, reason string) error {
	return b.sup.Call(ctx, "harness.shutdown", map[string]any{"reason": reason}, nil)
}

// EventQueue is an unbounded, non-blocking FIFO of sidecar events.
//
// Event notifications are handled on the bridge's read loop, so Push must
// never block: a stalled consumer (a slow SSE client) would otherwise freeze
// every other message, including the replies the turn itself is waiting for.
// Push therefore never blocks and never drops.
type EventQueue struct {
	mu     sync.Mutex
	items  []Event
	closed bool
	err    error
	wake   chan struct{}
}

// NewEventQueue returns an empty queue.
func NewEventQueue() *EventQueue {
	return &EventQueue{wake: make(chan struct{}, 1)}
}

// Push appends events and wakes a waiting Drain. It never blocks.
func (q *EventQueue) Push(events []Event) {
	if len(events) == 0 {
		return
	}
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, events...)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default: // a token is already pending; Drain will find the new events
	}
}

// Drain returns every event queued so far, waiting for at least one. It
// returns the close error (nil after a clean stop) once the queue is closed
// and empty.
func (q *EventQueue) Drain(ctx context.Context) ([]Event, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			out := q.items
			q.items = nil
			q.mu.Unlock()
			return out, nil
		}
		closed, err := q.closed, q.err
		q.mu.Unlock()
		if closed {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.wake:
		}
	}
}

// Close stops the queue. Drain reports err once the remaining events are
// consumed.
func (q *EventQueue) Close(err error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.err = err
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
