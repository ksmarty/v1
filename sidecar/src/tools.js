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
import { defineTool, hook, ToolTask, GenerationTask } from "@earendil-works/pi-durable";
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
	// A tool that produced pictures for the model (screenshot_app) attaches them
	// to its result; pi-ai sends them as image content blocks.
	if (Array.isArray(reply.images)) {
		for (const image of reply.images) {
			if (image && typeof image.data === "string") {
				result.content.push({ type: "image", data: image.data, mimeType: image.mimeType || "image/png" });
			}
		}
	}
	if (reply.details !== undefined) result.details = reply.details;
	return result;
}

/**
 * Build the host-tool registrations from the definitions Go sent.
 *
 * @param {{ call: (method: string, params: any, options?: any) => Promise<any> }} bridge
 * @param {readonly {name: string, description: string, parameters: object}[]} defs
 *        v1's tool definitions, in the model-facing shape
 * @param {(conversationId: string) => string} [resolveConversation]
 *        Maps the running conversation id to the one Go should run the tool for.
 *        A delegated child runs in its own conversation but must reach the
 *        parent turn's runner, so the host resolves it to the parent id; the
 *        default identity keeps ordinary turns unchanged.
 */
export function buildHostTools(bridge, defs, resolveConversation = (id) => id) {
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
					conversationId: String(resolveConversation(api.conversationId) ?? api.conversationId),
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
export function buildApprovalHook(bridge, resolveConversation = (id) => id) {
	return hook(ToolTask, {
		beforeTool: async (call, api) => {
			const decision = await bridge.call("tool.authorize", {
				tool: call.name,
				arguments: call.arguments,
				callId: String(api.taskId),
				conversationId: String(resolveConversation(api.conversationId) ?? api.conversationId),
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

/**
 * Continuation hook: a turn that ends after a thinking block with no answer is
 * not a finished turn. A reasoning model can fill its whole output window with
 * thinking (stopReason "length"), or stop right after a thinking block with no
 * text (stopReason "stop") — both read to the user as "the chat stopped midway
 * for no reason". pi-durable's onYield lets a hook append a user message and
 * hand the run to a successor generation, so this continues the same turn
 * durably, exactly like the built-in loop's truncation resume. Bounded, so a
 * provider stuck on "length" cannot spin forever.
 */
export function buildContinuationHook() {
	return hook(GenerationTask, {
		onYield: async (answer, api, context) => {
			const content = Array.isArray(answer.content) ? answer.content : [];
			if (content.some((part) => part?.type === "toolCall")) return undefined;
			const text = content
				.filter((part) => part?.type === "text")
				.map((part) => part.text ?? "")
				.join("")
				.trim();
			const truncated = answer.stopReason === "length";
			const hasThinking = content.some(
				(part) => part?.type === "thinking" && typeof part.thinking === "string" && part.thinking !== "",
			);
			// A clean answer is done; so is a completely empty response, which is a
			// provider fault (continuing would only re-ask with no new context).
			if (!truncated && (text !== "" || !hasThinking)) return undefined;
			const attempts = Number((await api.memo("v1.continuation", context)) ?? 0);
			if (attempts >= 3) {
				log.warn("continuation: still unfinished after several retries; keeping the partial reply", {
					stopReason: answer.stopReason,
				});
				return undefined;
			}
			await api.memo("v1.continuation", attempts + 1, context);
			log.info("continuation: the model stopped after thinking; continuing the turn", {
				stopReason: answer.stopReason,
				attempt: attempts + 1,
			});
			// A round that hit the output limit spent its budget on reasoning, so
			// ask for the answer directly instead of inviting more thinking; a
			// round that stopped cleanly after thinking just needs to continue.
			const message = truncated
				? "You reached the output limit while thinking. Do not reason further — answer the user's last message directly and concisely now."
				: "Continue from where you left off. Do not repeat what is already written above.";
			return { continue: message };
		},
	});
}
