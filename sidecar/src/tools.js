/**
 * Host tools (plan D4/F3): every agent tool stays implemented in Go. Each tool
 * here is a thin proxy — it declares the schema the model sees, then forwards
 * the call over the bridge and returns Go's result verbatim.
 *
 * Go owns argument validation, the path-escape/SSRF guards, output truncation
 * and TOON re-encoding, so nothing about tool semantics lives in this process.
 */
import { ToolTask, defineTool, hook } from "@earendil-works/pi-durable";
import { Type } from "typebox";

import { log } from "./log.js";

/**
 * Tool schemas for the walking skeleton. The authoritative argument structs are
 * v1's Go tool definitions; these mirror the names the model must send.
 */
const HOST_TOOLS = {
	read_file: {
		description:
			"Read a UTF-8 text file from the project. Returns the file contents, optionally a line range.",
		parameters: Type.Object({
			path: Type.String({ description: "Path to the file, relative to the project root or absolute" }),
			offset: Type.Optional(Type.Number({ description: "First line to return (1-based)" })),
			limit: Type.Optional(Type.Number({ description: "Maximum number of lines to return" })),
		}),
	},
	write_file: {
		description: "Write a UTF-8 text file in the project, creating parent directories as needed.",
		parameters: Type.Object({
			path: Type.String({ description: "Path to the file, relative to the project root or absolute" }),
			content: Type.String({ description: "Full file contents to write" }),
		}),
	},
	run_command: {
		description: "Run a shell command in the project directory and return its combined output.",
		parameters: Type.Object({
			command: Type.String({ description: "Command line to execute" }),
			timeout: Type.Optional(Type.Number({ description: "Timeout in seconds" })),
		}),
	},
};

/** Every tool name this sidecar knows how to proxy. */
export const HOST_TOOL_NAMES = Object.keys(HOST_TOOLS);

function toResult(reply) {
	if (!reply || typeof reply !== "object") {
		return { content: [{ type: "text", text: String(reply ?? "") }], isError: false };
	}
	const result = {
		content: [{ type: "text", text: typeof reply.text === "string" ? reply.text : "" }],
		isError: Boolean(reply.isError),
	};
	if (reply.details !== undefined) result.details = reply.details;
	return result;
}

/**
 * Build the host-tool registrations.
 *
 * @param {{ call: (method: string, params: any, options?: any) => Promise<any> }} bridge
 * @param {readonly string[]} [names] tools to expose; defaults to all known
 */
export function buildHostTools(bridge, names = HOST_TOOL_NAMES) {
	return names.map((name) => {
		const spec = HOST_TOOLS[name];
		if (!spec) throw new Error(`sidecar: no schema for host tool "${name}"`);
		return defineTool({
			name,
			description: spec.description,
			parameters: spec.parameters,
			execute: async (args, api) => {
				log.debug("tool call", { tool: name, callId: String(api.callId) });
				const reply = await bridge.call("tool.call", {
					tool: name,
					arguments: args,
					callId: String(api.callId),
					conversationId: String(api.conversationId),
				});
				return toResult(reply);
			},
		});
	});
}

/**
 * Approval hook: Go's permission registry decides allow / deny / ask, and may
 * rewrite the arguments. A non-empty `block` denies the call before it runs
 * (pi-durable records the intent, so a denied call still has a durable record).
 */
export function buildApprovalHook(bridge) {
	return hook(ToolTask, {
		beforeTool: async (call, api) => {
			const decision = await bridge.call("tool.authorize", {
				tool: call.name,
				arguments: call.arguments,
				callId: String(api.taskId),
				conversationId: String(api.conversationId),
				taskId: String(api.taskId),
			});
			if (!decision || typeof decision !== "object") return undefined;
			const outcome = {};
			if (decision.block) {
				log.info("tool denied", { tool: call.name });
				outcome.block = String(decision.block);
			}
			if (decision.arguments && typeof decision.arguments === "object") outcome.arguments = decision.arguments;
			return Object.keys(outcome).length ? outcome : undefined;
		},
	});
}
