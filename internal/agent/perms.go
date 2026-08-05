package agent

import "context"

// Resolver gates tool calls through the 3-option permission policy
// (allow / deny / ask). The server implements it: policy is read from
// settings, and an "ask" decision surfaces as an SSE permission_request that
// the user answers in the chat UI.
type Resolver interface {
	// Request resolves the policy for a tool call. It returns true when the
	// call may proceed, or an error describing the denial (policy deny, user
	// denied, or the request timed out).
	Request(ctx context.Context, tool, detail string) (bool, error)
}
