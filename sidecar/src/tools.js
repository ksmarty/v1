/**
 * Host tools (plan D4/F3): every agent tool stays implemented in Go. Each tool
 * here is a thin proxy — it takes the definition Go sent, then forwards the
 * call over the bridge and returns Go's result verbatim.
 *
 * Go owns the schemas, argument validation, the path-escape/SSRF guards, output
 * truncation and TOON re-encoding, so nothing about tool semantics lives in
 * this process. The sidecar deliberately keeps no schema of its own: the model
 * must see exactly what v1's built-in loop would have sent.
 */
import { defineTool, hook, ToolTask } from "@earendil-works/pi-durable";
import { Type } from "typebox";

import { log } from "./log.js";

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
 * Build the host-tool registrations from the definitions Go sent.
 *
 * @param {{ call: (method: string, params: any, options?: any) => Promise<any> }} bridge
 * @param {readonly {name: string, description: string, parameters: object}[]} defs
 *        v1's tool definitions, in the model-facing shape
 */
export function buildHostTools(bridge, defs) {
	return defs.map((def) => {
		const { name } = def;
		return defineTool({
			name,
			description: def.description,
			// Go holds plain JSON Schema; Unsafe hands it to the validator as-is
			// instead of making this process rebuild it as TypeBox calls.
			parameters: Type.Unsafe(def.parameters ?? { type: "object", properties: {} }),
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
