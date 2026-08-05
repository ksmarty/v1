// Package agent runs the chat agent loop: it streams completions from an
// OpenAI-compatible LLM and executes tool calls against a project workspace.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"v1/internal/llm"
	"v1/internal/store"
)

const systemPrompt = `You are v1, an AI full-stack engineer building web apps in the user's workspace directory.

Rules:
- The workspace already contains a project; prefer the existing project structure and conventions.
- All file paths are relative to the workspace root.
- After writing or changing code, call restart_preview so the user can see the result.
- Keep a visible todo list of your work using set_todos; add items up front and mark them done as they complete.
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
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Name      string       `json:"name,omitempty"`
	Detail    string       `json:"detail,omitempty"`
	OK        bool         `json:"ok,omitempty"`
	Error     string       `json:"error,omitempty"`
	Usage     *Usage       `json:"usage,omitempty"`
	Todos     []store.Todo `json:"todos,omitempty"`
	RequestID string       `json:"requestId,omitempty"`
	Tool      string       `json:"tool,omitempty"`
}

// ChatParams carries everything needed to run one chat turn.
type ChatParams struct {
	Store        *store.Store
	Project      *store.Project
	Client       *llm.Client
	Exec         *Executor
	Message      string
	Model        string      // per-turn override; empty uses p.Client.Model
	LastUserID   int64       // retry mode: >0 re-runs the existing user message
	ExtraTools   []llm.Tool  // dynamically added tools (e.g. MCP), namespaced
	SkillsPrompt string      // enabled skills' SKILL.md content for the system prompt
	Emit         func(ChatEvent)
}

// RunChat persists the user message, replays history to the LLM, executes
// tool calls (up to maxRounds rounds), persists the transcript (including the
// model, reasoning and usage) and returns the turn's final usage. The done
// event is emitted by the caller after RunChat returns.
func RunChat(ctx context.Context, p ChatParams) (*TurnResult, error) {
	if p.Model != "" {
		p.Client.Model = p.Model
	}
	if p.LastUserID > 0 {
		// Retry mode: the user message already exists with this ID; drop the
		// aborted turn that followed it so history is truncated at the user.
		if err := p.Store.DeleteMessagesAfter(p.Project.ID, p.LastUserID); err != nil {
			return nil, err
		}
	} else {
		if _, err := p.Store.AddMessage(p.Project.ID, "user", p.Message, "", p.Client.Model, "", ""); err != nil {
			return nil, err
		}
	}
	_ = p.Store.TouchProject(p.Project.ID)

	stored, err := p.Store.ListMessages(p.Project.ID)
	if err != nil {
		return nil, err
	}
	system := systemPrompt
	if p.SkillsPrompt != "" {
		system += "\n\n" + p.SkillsPrompt
	}
	history := []llm.Message{{Role: "system", Content: system}}
	for _, m := range stored {
		if p.LastUserID > 0 && m.ID > p.LastUserID {
			continue
		}
		switch m.Role {
		case "user":
			history = append(history, llm.Message{Role: "user", Content: m.Content})
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
			msg := llm.Message{Role: "tool", Content: m.Content}
			var tj struct {
				ToolCallID string `json:"tool_call_id"`
				Name       string `json:"name"`
			}
			if json.Unmarshal([]byte(m.ToolJSON), &tj) == nil {
				msg.ToolCallID = tj.ToolCallID
				msg.Name = tj.Name
			}
			history = append(history, msg)
		}
	}

	var usage *Usage
	for round := 0; round < maxRounds; round++ {
		allTools := tools
		if len(p.ExtraTools) > 0 {
			allTools = append(append([]llm.Tool{}, tools...), p.ExtraTools...)
		}
		res, err := p.Client.ChatStream(ctx, history, allTools,
			func(d string) { p.Emit(ChatEvent{Type: "delta", Text: d}) },
			func(d string) { p.Emit(ChatEvent{Type: "reasoning", Text: d}) })
		if err != nil {
			return nil, err
		}

		toolJSON := ""
		if len(res.ToolCalls) > 0 {
			b, _ := json.Marshal(map[string]any{"tool_calls": res.ToolCalls})
			toolJSON = string(b)
		}
		usageJSON := ""
		if res.Usage != nil {
			usage = &Usage{Input: res.Usage.PromptTokens, Output: res.Usage.CompletionTokens, Model: p.Client.Model}
			if b, err := json.Marshal(usage); err == nil {
				usageJSON = string(b)
			}
		}
		if _, err := p.Store.AddMessage(p.Project.ID, "assistant", res.Text, toolJSON, p.Client.Model, res.Reasoning, usageJSON); err != nil {
			return nil, err
		}
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
			if _, err := p.Store.AddMessage(p.Project.ID, "tool", result, string(tj), "", "", ""); err != nil {
				return nil, err
			}
			history = append(history, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}
	return &TurnResult{Usage: usage, Model: p.Client.Model}, nil
}

// toolDetail extracts a short human-readable detail for a tool call.
func toolDetail(tc llm.ToolCall) string {
	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
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
	return ""
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
