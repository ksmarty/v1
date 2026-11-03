package agent

import "encoding/json"

// User rows that the user did not type carry a tool_json marker saying who did.
// It must be VALID JSON — the chat API hands tool_json to the browser verbatim
// as json.RawMessage — and it is persisted, not inferred, so the transcript
// styles the row the same way after a reload or an app restart as it did while
// the turn was running.
const (
	// SteerToolJSON marks a mid-run message that was steered into a running
	// turn. The user did write it, but it joined a turn already in flight.
	SteerToolJSON = `"steer"`
	// AgentToolJSON marks text a tool injected on the agent's behalf — the
	// screenshot_app caption being the common one. The user did not write it,
	// and rendering it as their message made the transcript lie.
	AgentToolJSON = `"agent"`
)

// MarkerName decodes a stored marker into the bare name the client compares
// against. The constants above are raw JSON text (`"steer"`) because that is
// what tool_json holds, but a live event carries the name: handing the client
// the quoted form made it an unrecognised marker, and a steered row then
// rendered as one the agent had injected. Legacy bare markers pass through
// unchanged, so both encodings reach the client as the same name.
func MarkerName(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return raw
}

// IsSteerRow reports whether a stored tool_json marks a steered user message.
func IsSteerRow(toolJSON string) bool { return toolJSON == SteerToolJSON }

// IsAgentRow reports whether a stored tool_json marks a message the agent
// injected on the user's behalf.
func IsAgentRow(toolJSON string) bool { return toolJSON == AgentToolJSON }
