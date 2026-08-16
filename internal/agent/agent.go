// Package agent runs the chat agent loop: it streams completions from an
// OpenAI-compatible LLM and executes tool calls against a project workspace.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"v1/internal/llm"
	"v1/internal/store"
)

const systemPrompt = `You are v1, an AI full-stack engineer building web apps in the user's workspace directory.

Rules:
- The workspace already contains a project; prefer the existing project structure and conventions.
- All file paths are relative to the workspace root.
- The app preview runs inside an iframe on an insecure (http) proxied origin: crypto.randomUUID() is unavailable there and throws. Never use it in generated apps — generate ids with Math.random()/Date.now() or a counter instead.
- After writing or changing code, call restart_preview so the user can see the result.
- Keep a visible todo list of your work using set_todos; add items up front and mark them done as they complete.
- Save durable facts, decisions and user preferences with the remember tool; delete stale ones with forget.
- If something important is unclear or you need a decision, use ask_user instead of guessing.
- Keep your responses concise.`

const maxRounds = 15

// Usage is the token accounting attached to a finished turn.
type Usage struct {
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Model  string `json:"model"`
}

// TurnResult carries the outcome of one chat turn so the caller can attach
// usage (and the model actually used) to its done event.
type TurnResult struct {
	Usage *Usage
	Model string
}

// ChatEvent is one SSE event sent to the chat client.
type ChatEvent struct {
	Type        string           `json:"type"`
	Text        string           `json:"text,omitempty"`
	Name        string           `json:"name,omitempty"`
	Detail      string           `json:"detail,omitempty"`
	OK          bool             `json:"ok,omitempty"`
	Error       string           `json:"error,omitempty"`
	Usage       *Usage           `json:"usage,omitempty"`
	Todos       []store.Todo     `json:"todos,omitempty"`
	Memories    []store.Memory   `json:"memories,omitempty"`
	RequestID   string           `json:"requestId,omitempty"`
	Tool        string           `json:"tool,omitempty"`
	MessageID   int64            `json:"messageId,omitempty"`
	Attachments []AttachmentMeta `json:"attachments,omitempty"`
	Options     []string         `json:"options,omitempty"`
	Questions   []AskQuestion    `json:"questions,omitempty"`
}

// ChatParams carries everything needed to run one chat turn.
type ChatParams struct {
	Store            *store.Store
	Project          *store.Project
	Client           *llm.Client
	Exec             *Executor
	Message          string
	SessionID        string       // chat session the turn belongs to
	Attachments      []Attachment // files attached to this user turn
	Model            string       // per-turn override; empty uses p.Client.Model
	LastUserID       int64        // retry mode: >0 re-runs the existing user message
	ContinueFromID   int64        // continue mode: >0 resumes from this partial assistant message
	ExtraTools       []llm.Tool   // dynamically added tools (e.g. MCP), namespaced
	SkillsPrompt     string       // enabled skills' SKILL.md content for the system prompt
	MemoriesPrompt   string       // project memories section for the system prompt
	GlobalPrompt     string       // user's global system prompt (all projects)
	Vision           bool         // the model reads images — enables screenshot_app
	ReasoningEffort  string       // thinking level; sent as reasoning_effort when set
	ToonEnabled      bool         // tool results are TOON-encoded for the model
	Steer            func() []string // drains mid-run user messages, injected next round
	Background       *BackgroundManager
	// PollBackground returns the session's finished background commands so the
	// loop can inject their results into the conversation.
	PollBackground func() []BackgroundResult
	// BackgroundNotify persists a finished background command's result into
	// the transcript (wired by the server alongside Background).
	BackgroundNotify func(*BackgroundJob)
	SkipSnapshot     bool         // edits/retries rewind the thread — no git checkpoint
	PlanMode         bool         // read-only planning turn (/plan)
	RTKEnabled       bool         // run_command output is piped through RTK (when installed)
	ContextBudget    int
	ContextThreshold float64
	Summarizer       Summarizer
	Emit             func(ChatEvent)
}

// maxHistoricalToolResult is the largest a replayed (pre-turn) tool result may
// be in the request to the model. Larger historical results are elided to a
// one-line pointer: the model already processed them and the filesystem is
// the live source of truth, so the only cost is a leaner context. This keeps a
// long, tool-heavy conversation from exhausting the provider's output window
// and stalling mid-stream.
const maxHistoricalToolResult = 256

// planModeNote is injected into the system prompt while the user plans: the
// agent investigates with read-only tools and produces a plan, changing
// nothing.
const planModeNote = `Plan mode is active — the user asked you to plan, not to build. You must NOT modify files, run commands, restart previews, or change any state. Investigate the workspace with the read-only tools (list files, search, read file, fetch url), then present a concrete implementation plan: the approach, the files to create or change, and the steps in order.`

// freshProjectNote is injected into the system prompt while the workspace is
// still a blank slate (at most the scaffold README): the agent should treat
// the project as a brand-new start, not an existing codebase to fit into.
const freshProjectNote = `This is a brand-new project: this is the very first conversation and nothing has been built yet — the workspace holds nothing but a scaffold README at most. The rule about preferring the existing project structure does not apply here. Start from scratch: pick a sensible stack, scaffold the project, and build toward what the user asked for. If they already described what they want, begin building right away instead of asking clarifying questions first.`

// freshProject reports whether the workspace is still a blank slate: empty,
// or holding nothing but the scaffold README.md.
func freshProject(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != "README.md" {
			return false
		}
	}
	return true
}

// RunChat persists the user message, replays history to the LLM, executes
// tool calls (up to maxRounds rounds), persists the transcript (including the
// model, reasoning and usage) and returns the turn's final usage. The done
// event is emitted by the caller after RunChat returns.
func RunChat(ctx context.Context, p ChatParams) (*TurnResult, error) {
	if p.Model != "" {
		p.Client.Model = p.Model
	}
	if p.ReasoningEffort != "" {
		p.Client.ReasoningEffort = p.ReasoningEffort
	}
	if p.LastUserID > 0 {
		// Retry mode: the user message already exists with this ID; drop the
		// aborted turn that followed it so history is truncated at the user.
		if err := p.Store.DeleteMessagesAfter(p.Project.ID, p.SessionID, p.LastUserID); err != nil {
			return nil, err
		}
	} else if p.ContinueFromID <= 0 {
		if _, err := p.Store.AddMessage(p.Project.ID, p.SessionID, "user", p.Message, "", p.Client.Model, "", "", MarshalAttachments(p.Attachments)); err != nil {
			return nil, err
		}
	}
	_ = p.Store.TouchProject(p.Project.ID)

	stored, err := p.Store.ListMessages(p.Project.ID, p.SessionID)
	if err != nil {
		return nil, err
	}
	system := systemPrompt
	if snapshot, snapshotErr := p.Store.GetCompactionSnapshot(p.Project.ID, p.SessionID); snapshotErr == nil {
		system += "\n\nConversation summary (historical, not user-visible; covers messages through ID " + fmt.Sprint(snapshot.CoveredMessageID) + "):\n" + snapshot.Summary
	}
	if p.SkillsPrompt != "" {
		system += "\n\n" + p.SkillsPrompt
	}
	if p.MemoriesPrompt != "" {
		system += "\n\n" + p.MemoriesPrompt
	}
	if freshProject(p.Project.Path) {
		system += "\n\n" + freshProjectNote
	}
	if p.PlanMode {
		system += "\n\n" + planModeNote
	}
	if p.Project.Instructions != "" {
		system += "\n\nProject instructions from the user:\n" + p.Project.Instructions
	}
	if p.GlobalPrompt != "" {
		system += "\n\nGlobal instructions from the user (apply to all projects):\n" + p.GlobalPrompt
	}
	if p.ToonEnabled {
		system += "\n- Tool results are encoded in TOON, a compact indentation format: headers like key[N]{fields}: declare array length and column names, and rows are comma-separated. Read them as structured data, not prose."
	}
	if p.RTKEnabled {
		system += "\n- run_command output is passed through RTK, which compresses or summarizes common commands (test runners, git, builds): failures and errors are preserved, successful output is condensed. When a failure log was truncated, RTK prints a [full output: …] path you can read with read_file. If RTK ever mangles or hides output you need, rerun the command with \"rtk\": false to get the raw output."
	}
	if p.Background != nil {
		system += "\n- run_command_background starts a command detached from the turn: it returns a job id immediately, and the result is injected into the chat as a user message (\"[Background #id: command] finished (exit N): output\") once the command completes — mid-turn if you are still working, or at the start of your next turn. Start long-running work with it instead of blocking, and react to the result when it arrives."
	}
	if p.Vision {
		system += "\n\nYou can see the app: call screenshot_app to capture an image of the running preview and inspect what is on screen. Use it after visual changes to verify them."
	}
	history := []llm.Message{{Role: "system", Content: system}}
	p.Exec.PlanMode = p.PlanMode
	p.Exec.RTKEnabled = p.RTKEnabled
	p.Exec.Background = p.Background
	p.Exec.BackgroundNotify = p.BackgroundNotify
	p.Exec.SessionID = p.SessionID
	var coveredID int64
	if snapshot, snapshotErr := p.Store.GetCompactionSnapshot(p.Project.ID, p.SessionID); snapshotErr == nil {
		coveredID = snapshot.CoveredMessageID
	}
	for _, m := range stored {
		if m.ID <= coveredID || (p.LastUserID > 0 && m.ID > p.LastUserID) || (p.ContinueFromID > 0 && m.ID > p.ContinueFromID) {
			continue
		}
		switch m.Role {
		case "user":
			if m.Attachments != "" {
				history = append(history, userMessage(m.Content, ParseAttachments(m.Attachments)))
			} else {
				history = append(history, llm.Message{Role: "user", Content: m.Content})
			}
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
			history = append(history, msg)
		case "tool":
			msg := llm.Message{Role: "tool"}
			var tj struct {
				ToolCallID string `json:"tool_call_id"`
				Name       string `json:"name"`
			}
			if json.Unmarshal([]byte(m.ToolJSON), &tj) == nil {
				msg.ToolCallID = tj.ToolCallID
				msg.Name = tj.Name
			}
			// Elide oversized replayed tool results so a long, tool-heavy
			// conversation doesn't exhaust the provider's output window and
			// stall mid-stream. The model already acted on these historical
			// results; the workspace files remain the live source of truth.
			toolContent := elideHistoricalToolResult(msg.Name, m.Content)
			if p.ToonEnabled {
				toolContent = toonJSON(toolContent)
			}
			msg.Content = toolContent
			history = append(history, msg)
		}
	}

	var usage *Usage
	// When the provider's output window runs out mid-reply (finish_reason
	// "length"), the partial stays in the model's view and a retry continues
	// it instead of regenerating from scratch; the accumulated text is folded
	// into the persisted reply so a cap-hit never reads as a finished turn or
	// an empty one.
	var partialText, partialReasoning string
	assistantSaved := false
	vision := hasImageParts(history)
	// Continue mode: the history ends with the partial assistant reply — ask
	// the model to pick up where it stopped instead of repeating itself.
	if p.ContinueFromID > 0 {
		history = append(history, llm.Message{
			Role:    "user",
			Content: "Continue from where you left off. Do not repeat what is already written above.",
		})
	}
	for round := 0; round < maxRounds; round++ {
		// Steered messages join the turn between rounds: persisted like a
		// normal user turn and rendered in the UI via injected_message.
		if p.Steer != nil {
			for _, msg := range p.Steer() {
				msgID, err := p.Store.AddMessage(p.Project.ID, p.SessionID, "user", msg, "", p.Client.Model, "", "", "")
				if err != nil {
					return nil, err
				}
				history = append(history, llm.Message{Role: "user", Content: msg})
				p.Emit(ChatEvent{Type: "injected_message", MessageID: msgID, Text: msg})
			}
		}
		// Finished background commands join the same way: their result was
		// already persisted by the completion callback, so this just appends
		// it to the model's view and surfaces it in the UI.
		if p.PollBackground != nil {
			for _, r := range p.PollBackground() {
				history = append(history, llm.Message{Role: "user", Content: r.Text})
				p.Emit(ChatEvent{Type: "injected_message", MessageID: r.MessageID, Text: r.Text})
			}
		}
		allTools := tools
		if p.Vision {
			allTools = append(append([]llm.Tool{}, allTools...), screenshotAppTool)
		}
		if len(p.ExtraTools) > 0 {
			allTools = append(append([]llm.Tool{}, allTools...), p.ExtraTools...)
		}
		if p.PlanMode {
			allTools = planSafeTools(allTools)
		}
		requestHistory := compactForModel(ctx, history, p)
		res, err := p.Client.ChatStream(ctx, requestHistory, allTools,
			func(d string) { p.Emit(ChatEvent{Type: "delta", Text: d}) },
			func(d string) { p.Emit(ChatEvent{Type: "reasoning", Text: d}) })
		// Models that don't support vision reject image parts; retry once with
		// the images replaced by a text note.
		if err != nil && round == 0 && vision {
			history = stripImageParts(history)
			vision = false
			res, err = p.Client.ChatStream(ctx, compactForModel(ctx, history, p), allTools,
				func(d string) { p.Emit(ChatEvent{Type: "delta", Text: d}) },
				func(d string) { p.Emit(ChatEvent{Type: "reasoning", Text: d}) })
		}
		if err != nil {
			// A mid-response failure (token limit, network drop) still leaves
			// the streamed partial reply — persist it so the transcript shows
			// what made it out and a retry can continue from here instead of
			// regenerating everything.
			if res != nil && (res.Text != "" || res.Reasoning != "") {
				_, _ = p.Store.AddMessage(p.Project.ID, p.SessionID, "assistant", res.Text, "", p.Client.Model, res.Reasoning, "", "")
			}
			return nil, err
		}
		// Some providers stream tool calls without ids; they only need to be
		// unique within the conversation, so fill in a local one — otherwise the
		// tool result goes out without tool_call_id and strict providers 400.
		for i := range res.ToolCalls {
			if res.ToolCalls[i].ID == "" {
				res.ToolCalls[i].ID = fmt.Sprintf("v1_call_%d_%d", round, i)
			}
		}

		toolJSON := ""
		if len(res.ToolCalls) > 0 {
			b, _ := json.Marshal(map[string]any{"tool_calls": res.ToolCalls})
			toolJSON = string(b)
		}
		// Every LLM call in a turn bills its full prompt (the replayed history
		// grows with each round), so the turn total is the sum across rounds —
		// not the last round's numbers, which is what a naive overwrite gives.
		usageJSON := ""
		if res.Usage != nil {
			if usage == nil {
				usage = &Usage{Model: p.Client.Model}
			}
			usage.Input += res.Usage.PromptTokens
			usage.Output += res.Usage.CompletionTokens
			usage.Model = p.Client.Model
			if b, err := json.Marshal(usage); err == nil {
				usageJSON = string(b)
			}
		}
		// If the provider's output window ran out mid-reply with no tool call
		// to continue from (finish_reason "length" — the classic cause of
		// "chats stop prematurely"), don't persist the truncated partial and
		// don't treat it as a completed turn. Retry the round; the next call
		// has no partial to play back and can complete normally. The round
		// budget bounds retries, so this can't loop forever.
		truncated := len(res.ToolCalls) == 0 && res.StopReason == "length"
		if truncated {
			if res.Text != "" || res.Reasoning != "" {
				partialText += res.Text
				if partialReasoning != "" && res.Reasoning != "" {
					partialReasoning += "\n"
				}
				partialReasoning += res.Reasoning
				history = append(history, llm.Message{Role: "assistant", Content: res.Text, ReasoningContent: res.Reasoning})
				history = append(history, llm.Message{Role: "user", Content: "Continue from where you left off. Do not repeat what is already written above."})
			}
			p.Emit(ChatEvent{Type: "info", Text: "Output window hit mid-reply; continuing turn."})
			continue
		}

		// A provider can end a stream successfully without producing anything
		// (a gateway error body, a quota wall responding 200) — never treat
		// that as a completed turn, or the chat ends prematurely with empty
		// replies and no explanation.
		if res.Text == "" && res.Reasoning == "" && len(res.ToolCalls) == 0 {
			return nil, fmt.Errorf("LLM returned an empty response — the provider may have rejected the request or run out of quota")
		}

		// Fold any truncated partials into this message so the persisted reply
		// matches what the model produced (UI-streamed) end to end.
		persistText, persistReasoning := res.Text, res.Reasoning
		if partialText != "" {
			if persistText != "" {
				persistText = strings.TrimRight(partialText, "\n") + "\n\n" + strings.TrimSpace(persistText)
			} else {
				persistText = partialText
			}
			if persistReasoning != "" {
				persistReasoning = strings.TrimRight(partialReasoning, "\n") + "\n" + strings.TrimSpace(persistReasoning)
			} else {
				persistReasoning = partialReasoning
			}
			partialText, partialReasoning = "", ""
		}
		if _, err := p.Store.AddMessage(p.Project.ID, p.SessionID, "assistant", persistText, toolJSON, p.Client.Model, persistReasoning, usageJSON, ""); err != nil {
			return nil, err
		}
		assistantSaved = true
		history = append(history, llm.Message{Role: "assistant", Content: res.Text, ReasoningContent: res.Reasoning, ToolCalls: res.ToolCalls})

		if len(res.ToolCalls) == 0 {
			return &TurnResult{Usage: usage, Model: p.Client.Model}, nil
		}
		for _, tc := range res.ToolCalls {
			p.Emit(ChatEvent{Type: "tool_start", Name: tc.Function.Name, Detail: toolDetail(tc)})
			result, execErr := p.Exec.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
			ok := execErr == nil
			if !ok {
				result = "error: " + execErr.Error()
			}
			summary := result
			if len(summary) > 300 {
				summary = summary[:300] + "..."
			}
			p.Emit(ChatEvent{Type: "tool_end", Name: tc.Function.Name, OK: ok, Detail: summary})
			tj, _ := json.Marshal(map[string]any{"tool_call_id": tc.ID, "name": tc.Function.Name})
			if _, err := p.Store.AddMessage(p.Project.ID, p.SessionID, "tool", result, string(tj), "", "", "", ""); err != nil {
				return nil, err
			}
			content := result
			if p.ToonEnabled {
				content = toonJSON(result)
			}
			history = append(history, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    content,
			})
			// A screenshot tool call leaves a PNG behind: deliver it as a user
			// message with an image part (tool results are text-only on many
			// OpenAI-compatible APIs). Persisted like a user attachment so
			// history replay and the chat UI show it too.
			if img := p.Exec.PendingImage; len(img) > 0 {
				p.Exec.PendingImage = nil
				att := []Attachment{{
					Name:    "screenshot.png",
					MIME:    "image/png",
					Kind:    "image",
					Content: base64.StdEncoding.EncodeToString(img),
				}}
				caption := "Screenshot of the current app preview (captured by the screenshot_app tool)."
				msgID, err := p.Store.AddMessage(p.Project.ID, p.SessionID, "user", caption, "", "", "", "", MarshalAttachments(att))
				if err != nil {
					return nil, err
				}
				history = append(history, userMessage(caption, att))
				p.Emit(ChatEvent{
					Type:        "injected_message",
					MessageID:   msgID,
					Text:        caption,
					Attachments: []AttachmentMeta{{Name: "screenshot.png", MIME: "image/png", Kind: "image", Size: len(img)}},
				})
			}
		}
	}
	// The round budget ran out while the response was still being truncated —
	// persist what made it out so the turn never silently vanishes.
	if !assistantSaved && (partialText != "" || partialReasoning != "") {
		_, _ = p.Store.AddMessage(p.Project.ID, p.SessionID, "assistant", partialText, "", p.Client.Model, partialReasoning, "", "")
	}
	return &TurnResult{Usage: usage, Model: p.Client.Model}, nil
}

// elideHistoricalToolResult returns the value a replayed tool result should
// carry in the request to the model. Results under the cap pass through whole;
// larger ones collapse to a compact pointer that preserves the tool name and
// size without paying the full context cost.
func elideHistoricalToolResult(name, content string) string {
	if content == "" {
		return ""
	}
	if len(content) <= maxHistoricalToolResult {
		return content
	}
	return fmt.Sprintf("[%s result omitted — %d bytes; re-read the file if you need it again]", name, len(content))
}

// toolDetail extracts a short human-readable detail for a tool call.
func toolDetail(tc llm.ToolCall) string {
	var args struct {
		Path     string `json:"path"`
		Command  string `json:"command"`
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	if args.Path != "" {
		return args.Path
	}
	if args.Command != "" {
		if len(args.Command) > 120 {
			return args.Command[:120] + "..."
		}
		return args.Command
	}
	if args.Question != "" {
		if len(args.Question) > 120 {
			return args.Question[:120] + "..."
		}
		return args.Question
	}
	return ""
}

var screenshotAppTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "screenshot_app",
		Description: "Capture a screenshot of the app preview so you can see its current visual state. Starts the preview if it is not running. Only available for models that can read images.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Route path inside the app to capture (e.g. /about). Defaults to the app's root page.",
				},
			},
		},
	},
}

var tools = []llm.Tool{
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "list_files",
			Description: "List files and directories in the workspace as a recursive tree (max depth 3). node_modules, .git and dist are skipped.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to list, relative to the workspace root. Defaults to the root.",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "search_files",
			Description: "Search the workspace for files or code: semantic similarity via semble when installed, otherwise filename (fd) and content (rg) matches. Use this to find a file by name or locate code by identifier, phrase, or concept instead of guessing paths.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search text: a natural-language description of what you are looking for, or an exact string/identifier.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Subdirectory to search, relative to the workspace root. Defaults to the root.",
					},
				},
				"required": []string{"query"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "read_file",
			Description: "Read the contents of a file in the workspace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to the workspace root.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "write_file",
			Description: "Create or overwrite a file in the workspace. Parent directories are created as needed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to the workspace root.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full file content to write.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "edit_file",
			Description: "Replace an exact string in a file with a new string.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to the workspace root.",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "Exact text to replace.",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "Replacement text.",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "delete_file",
			Description: "Delete a file from the workspace (files only, never directories).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to the workspace root.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "move_file",
			Description: "Rename or move a file or directory within the workspace. Destination parent directories are created as needed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Current path relative to the workspace root.",
					},
					"newPath": map[string]any{
						"type":        "string",
						"description": "Destination path relative to the workspace root.",
					},
				},
				"required": []string{"path", "newPath"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "fetch_url",
			Description: "Fetch a web page (http/https) and return its readable text — docs, READMEs, API references. HTML is stripped to text; responses are size-capped.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Full http(s) URL to fetch.",
					},
				},
				"required": []string{"url"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "run_command",
			Description: "Run a shell command in the workspace (sh -c). Returns the exit code and combined output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute.",
					},
					"timeout_seconds": map[string]any{
						"type":        "number",
						"description": "Timeout in seconds (default 120, max 600).",
					},
					"rtk": map[string]any{
						"type":        "boolean",
						"description": "Run without RTK output compression for this command. Set false when RTK mangles or hides output you need; defaults to true when RTK is enabled.",
					},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "restart_preview",
			Description: "(Re)start the app preview so the user can see the current state of the app. Returns the preview URL.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "run_command_background",
			Description: "Run a shell command in the workspace detached from the turn. Returns immediately with a job id; the result arrives later in the chat as a \"[Background #id: …] finished…\" message, which you can react to. Use for long-running commands (installs, servers, long tests) when you have other work to do first.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute.",
					},
					"timeout_seconds": map[string]any{
						"type":        "number",
						"description": "Timeout in seconds (default 600, max 3600).",
					},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "set_project_name",
			Description: "Set the project's display name. Call it at the start of a new project to give it a short, descriptive name based on what you are building.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The new project name (3-80 characters).",
					},
				},
				"required": []string{"name"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "set_todos",
			Description: "Maintain the task list shown to the user. Pass the full desired list as an array of {title, done} objects (not a delta); items are shown in order. Call it to create todos at the start of a task and to mark items done as they complete.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"title": map[string]any{"type": "string"},
								"done":  map[string]any{"type": "boolean"},
							},
							"required": []string{"title"},
						},
					},
				},
				"required": []string{"todos"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "remember",
			Description: "Save a fact, decision or user preference to this project's long-term memory. It will be shown back to you in future turns. Keep it to one short sentence.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "The fact to remember.",
					},
				},
				"required": []string{"content"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "forget",
			Description: "Delete one project memory by its id (ids are listed in the system prompt's memories section).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "number",
						"description": "The memory id to delete.",
					},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "ask_user",
			Description: "Ask the user one or more questions and wait for their answers. Use when you need decisions, clarifications or preferences instead of guessing. Pass a single question in \"question\" (with optional \"options\"); to ask several in sequence, pass them in \"questions\" — the user steps through them and confirms all answers at once.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{
						"type":        "string",
						"description": "The question to ask the user (when asking a single question).",
					},
					"options": map[string]any{
						"type":        "array",
						"description": "Optional 2-4 suggested answers for a single question.",
						"items":       map[string]any{"type": "string"},
					},
					"questions": map[string]any{
						"type": "array",
						"description": "Multiple questions to ask in sequence (2-8). Each has a question and optional 2-4 options.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"question": map[string]any{"type": "string"},
								"options": map[string]any{
									"type":  "array",
									"items": map[string]any{"type": "string"},
								},
							},
							"required": []string{"question"},
						},
					},
				},
			},
		},
	},
}

// toolResult marshals a tool result map to the JSON string fed back to the LLM.
func toolResult(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// truncate shortens s to n bytes with a notice.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(s))
}

// trimHeadTail keeps the first and last n bytes of s.
func trimHeadTail(s string, n int) string {
	if len(s) <= 2*n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n...[truncated %d bytes]...\n", len(s)-2*n) + s[len(s)-n:]
}

// looksBinary reports whether b contains a NUL byte.
func looksBinary(b []byte) bool {
	return strings.Contains(string(b), "\x00")
}
