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

/** How long a burst of continuations lasts before it is treated as a new turn. */
const CONTINUATION_WINDOW_MS = 5 * 60 * 1000;
/** Most continuations one turn may take before the partial reply is kept. */
const MAX_CONTINUATIONS = 3;
/** Process-local continuation counts, keyed by conversation id. */
const continuationAttempts = new Map();

/**
 * The continuation prompt for a model round, or null when the round is a
 * finished answer. A round with visible text is done; a round with no text is
 * never finished — it hit the output limit, stopped after a thinking block, or
 * returned nothing at all right after a tool call (which reads as the chat
 * stopping for no reason).
 */
export function continuationMessage(content, stopReason) {
	const parts = Array.isArray(content) ? content : [];
	if (parts.some((part) => part?.type === "toolCall")) return null;
	const text = parts
		.filter((part) => part?.type === "text")
		.map((part) => part.text ?? "")
		.join("")
		.trim();
	if (stopReason !== "length" && text !== "") return null;
	const hasThinking = parts.some(
		(part) => part?.type === "thinking" && typeof part.thinking === "string" && part.thinking !== "",
	);
	if (stopReason === "length") {
		return "You reached the output limit while thinking. Do not reason further — answer the user's last message directly and concisely now.";
	}
	return hasThinking
		? "Continue from where you left off. Do not repeat what is already written above."
		: "You stopped without answering. Answer the user's last message directly and concisely now.";
}

/**
 * A provider that ends a tool-calling round with "length" (the model ran out of
 * output while writing the call) or "stop" leaves pi-durable nothing to run: it
 * only starts a tool round for exactly "toolUse", so the call is committed and
 * then never executed — the turn ends with a tool call and no result, which the
 * model itself reads as "the remember tool keeps getting interrupted". Promote
 * such a message back to a tool round so the call actually runs. An "error"
 * stop reason is left alone; that path has its own retry/compaction handling.
 */
export function promoteToolRound(message) {
	if (
		(message.stopReason === "length" || message.stopReason === "stop") &&
		Array.isArray(message.content) &&
		message.content.some((part) => part?.type === "toolCall")
	) {
		message.stopReason = "toolUse";
		return true;
	}
	return false;
}

/**
 * Continuation hook: a turn that ends with no visible answer is not a finished
 * turn. A reasoning model can fill its whole output window with thinking
 * (stopReason "length"), stop right after a thinking block with no text, or —
 * right after a tool call such as remember — return nothing at all. All of
 * those read to the user as "the chat stopped midway for no reason".
 * pi-durable's onYield lets a hook append a user message and hand the run to a
 * successor generation, so this continues the same turn durably, exactly like
 * the built-in loop's truncation resume. Bounded, so a provider stuck on empty
 * cannot spin forever.
 */
export function buildContinuationHook() {
	return hook(GenerationTask, {
		// Runs before pi-durable classifies the message, so a tool call the provider
		// ended with the wrong stop reason is promoted to a real tool round.
		afterResponse: (message) => {
			const original = message.stopReason;
			if (promoteToolRound(message)) {
				log.info("tool round: running a tool call the provider did not mark as toolUse", {
					stopReason: original,
				});
			}
		},
		onYield: async (answer, api, context) => {
			const message = continuationMessage(answer.content, answer.stopReason);
			if (message === null) return undefined;
			// pi-durable's per-task memo resets when a continuation hands the run to a
			// successor generation, so it cannot bound this. A process-local counter,
			// reset by a gap between turns, can.
			const key = String(api.conversationId ?? "");
			const now = Date.now();
			const prev = continuationAttempts.get(key);
			const attempts = prev !== undefined && now - prev.at < CONTINUATION_WINDOW_MS ? prev.attempts : 0;
			if (attempts >= MAX_CONTINUATIONS) {
				continuationAttempts.delete(key);
				log.warn("continuation: still unfinished after several retries; keeping the partial reply", {
					stopReason: answer.stopReason,
				});
				return undefined;
			}
			continuationAttempts.set(key, { attempts: attempts + 1, at: now });
			if (continuationAttempts.size > 500) {
				for (const [id, state] of continuationAttempts) {
					if (now - state.at >= CONTINUATION_WINDOW_MS) continuationAttempts.delete(id);
				}
			}
			log.info("continuation: the model produced no answer; continuing the turn", {
				stopReason: answer.stopReason,
				attempt: attempts + 1,
			});
			return { continue: message };
		},
	});
}

/**
 * Tokenize a query or a tool's name/description for the catalog search. The
 * catalog is small (tens of entries), so a token-overlap score is enough and
 * avoids pulling in a search dependency.
 */
function tokenize(text) {
	return String(text ?? "")
		.toLowerCase()
		.split(/[^a-z0-9_]+/)
		.filter(Boolean);
}

function scoreTool(tool, terms) {
	const name = String(tool.name ?? "").toLowerCase();
	const description = String(tool.description ?? "").toLowerCase();
	let score = 0;
	for (const term of terms) {
		if (name === term) score += 8;
		else if (name.includes(term)) score += 4;
		if (description.includes(term)) score += 1;
	}
	return score;
}

/**
 * Rank a deferred-tool catalog against a query. Name matches outweigh
 * description matches so `run_command` beats a tool that merely mentions
 * "command" in prose.
 */
export function searchTools(catalog, query, limit = 8) {
	const terms = [...new Set(tokenize(query))];
	if (terms.length === 0) return [];
	const count = Number.isFinite(limit) ? Math.max(1, Math.min(Math.trunc(limit), 20)) : 8;
	return catalog
		.map((tool) => ({ tool, score: scoreTool(tool, terms) }))
		.filter((entry) => entry.score > 0)
		.sort((a, b) => b.score - a.score || String(a.tool.name).localeCompare(String(b.tool.name)))
		.slice(0, count)
		.map((entry) => entry.tool);
}

/**
 * The deferred-tool search. In "search" exposure mode the model sees a small
 * core set; this tool ranks the hidden catalog and asks pi-durable to add the
 * matches to the conversation, so their schemas are sent on the next round.
 *
 * @param {() => readonly {name: string, description?: string}[]} catalog
 *        The currently hidden tools, resolved lazily so a later extension
 *        reload is reflected without reinstalling this tool.
 * @param {(conversationId: string, names: readonly string[]) => void} onMatch
 *        Records the revealed names so a later turn keeps them exposed.
 */
export function buildSearchTool(catalog, onMatch) {
	return defineTool({
		name: "search_tools",
		description:
			"Search for additional tools that are not loaded yet (extension and MCP tools). " +
			"Call this when a task needs a capability you do not currently see. The matching " +
			"tools become available for your next action.",
		parameters: Type.Object({
			query: Type.String({ description: "What you want to do, in a few words." }),
			limit: Type.Optional(Type.Number({ description: "How many tools to reveal (1-20, default 8)." })),
		}),
		execute: async (args, api) => {
			const query = String(args?.query ?? "").trim();
			const matches = searchTools(catalog(), query, args?.limit);
			if (matches.length === 0) {
				return { content: [{ type: "text", text: `No tools matched "${query}". Try different words.` }] };
			}
			const names = matches.map((tool) => tool.name);
			onMatch(String(api.conversationId), names);
			const lines = matches.map((tool) => `- ${tool.name}: ${tool.description ?? ""}`.trim());
			return {
				content: [{ type: "text", text: `These tools are now available for your next action:\n${lines.join("\n")}` }],
				control: { addTools: names },
			};
		},
	});
}
