package extensions

import "time"

// Builtins are the extensions v1 ships. Their source is written to the
// extensions root on startup, so they behave exactly like a user's extension:
// they can be read, edited, disabled or deleted from the settings UI.
func Builtins() []Builtin {
	return []Builtin{delegateBuiltin}
}

// delegateBuiltin adds a sub-agent tool.
//
// It is deliberately thin: the child conversation is created by the host (see
// the sidecar's `delegate`), because only the host holds the harness and the
// asking turn's agent. This module is the tool definition and the prompt-facing
// description.
var delegateBuiltin = Builtin{
	Extension: Extension{
		ID:          "delegate",
		Name:        "delegate",
		Description: "Run a task in a separate sub-agent conversation and return its final answer.",
		Enabled:     true,
		Builtin:     true,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	},
	Source: `/**
 * Delegate a self-contained task to a sub-agent.
 *
 * The sub-agent gets a fresh conversation with no history, so the task has to
 * carry everything it needs. It shares this conversation's model, working
 * directory and instructions, and its final message is returned to the caller.
 */
export default (pi) => ({
	name: "delegate",
	tools: [
		pi.defineTool({
			name: "delegate",
			description:
				"Run a task in a separate sub-agent conversation and return its final answer. " +
				"Use this when a job is self-contained and would otherwise flood this conversation " +
				"with intermediate output, such as a broad search, a review, or a multi-step " +
				"investigation. The sub-agent starts with no history and cannot see this " +
				"conversation, so state everything it needs in the task. It reports only its final " +
				"message, so ask it to summarise what it found.",
			parameters: {
				type: "object",
				properties: {
					task: {
						type: "string",
						description:
							"The complete task for the sub-agent. Include all necessary context, " +
							"since it cannot see this conversation.",
					},
				},
				required: ["task"],
			},
			async execute(args, _api, context) {
				const task = typeof args?.task === "string" ? args.task.trim() : "";
				if (!task) {
					return { content: [{ type: "text", text: "delegate: a task is required." }] };
				}
				const result = await pi.delegate({ task }, context);
				const summary =
					result.toolCalls > 0
						? "Sub-agent used " + result.toolCalls + " tool call(s)."
						: "Sub-agent used no tools.";
				const body = result.text || "(the sub-agent produced no text)";
				// The trailing marker lets v1 offer the sub-agent's full transcript;
				// the UI strips it before display.
				const marker = result.conversationId
					? "\n\n[v1-subagent:" + result.conversationId + "]"
					: "";
				return { content: [{ type: "text", text: summary + "\n\n" + body + marker }] };
			},
		}),
	],
	// The tool description says what delegate does; this says when to reach for
	// it. Without it the model tends to do every search and review itself, and
	// the conversation fills with output nobody reads.
	sections: [
		pi.section(
			"delegate",
			() =>
				"Delegation: when a job would flood this conversation with intermediate output " +
				"- a broad search across the codebase, a review of a large diff, a multi-step " +
				"investigation - call delegate with a complete, self-contained task instead of " +
				"doing it here. The sub-agent cannot see this conversation, so state everything " +
				"it needs and ask it to summarise what it found. Delegate the reading, searching " +
				"and reviewing; do the editing yourself.",
			{ tag: false },
		),
	],
});
`,
}
