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
- Keep your responses concise.`

const maxRounds = 15

// ChatEvent is one SSE event sent to the chat client.
type ChatEvent struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ChatParams carries everything needed to run one chat turn.
type ChatParams struct {
	Store   *store.Store
	Project *store.Project
	Client  *llm.Client
	Exec    *Executor
	Message string
	Emit    func(ChatEvent)
}

// RunChat persists the user message, replays history to the LLM, executes
// tool calls (up to maxRounds rounds) and persists the transcript. The done
// event is emitted by the caller after RunChat returns nil.
func RunChat(ctx context.Context, p ChatParams) error {
	if _, err := p.Store.AddMessage(p.Project.ID, "user", p.Message, ""); err != nil {
		return err
	}
	_ = p.Store.TouchProject(p.Project.ID)

	stored, err := p.Store.ListMessages(p.Project.ID)
	if err != nil {
		return err
	}
	history := []llm.Message{{Role: "system", Content: systemPrompt}}
	for _, m := range stored {
		switch m.Role {
		case "user":
			history = append(history, llm.Message{Role: "user", Content: m.Content})
		case "assistant":
			msg := llm.Message{Role: "assistant", Content: m.Content}
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

	for round := 0; round < maxRounds; round++ {
		res, err := p.Client.ChatStream(ctx, history, tools, func(d string) {
			p.Emit(ChatEvent{Type: "delta", Text: d})
		})
		if err != nil {
			return err
		}

		toolJSON := ""
		if len(res.ToolCalls) > 0 {
			b, _ := json.Marshal(map[string]any{"tool_calls": res.ToolCalls})
			toolJSON = string(b)
		}
		if _, err := p.Store.AddMessage(p.Project.ID, "assistant", res.Text, toolJSON); err != nil {
			return err
		}
		history = append(history, llm.Message{Role: "assistant", Content: res.Text, ToolCalls: res.ToolCalls})

		if len(res.ToolCalls) == 0 {
			return nil
		}
		for _, tc := range res.ToolCalls {
			p.Emit(ChatEvent{Type: "tool_start", Name: tc.Function.Name, Detail: toolDetail(tc)})
			result, execErr := p.Exec.Execute(tc.Function.Name, tc.Function.Arguments)
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
			if _, err := p.Store.AddMessage(p.Project.ID, "tool", result, string(tj)); err != nil {
				return err
			}
			history = append(history, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}
	return nil
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
