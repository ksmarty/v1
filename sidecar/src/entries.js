/**
 * Helpers for reading pi-durable entries pages.
 *
 * `Conversation.entries()` returns entries newest-first. A delegated sub-agent
 * runs several rounds, so its tool calls live in the older entries; a scan that
 * stops at the final assistant text would count none of them and report "no
 * tools used". These helpers scan the whole page instead.
 */

/** True when a message content block is a tool call. */
function isToolCallBlock(block) {
	return typeof block?.type === "string" && block.type.toLowerCase().startsWith("toolcall");
}

/** Counts the tool calls across a page of entries (any order). */
export function countToolCalls(items) {
	let toolCalls = 0;
	for (const entry of items ?? []) {
		for (const message of entry?.model ?? []) {
			if (message?.role !== "assistant") continue;
			const blocks = Array.isArray(message.content) ? message.content : [];
			for (const block of blocks) {
				if (isToolCallBlock(block)) toolCalls += 1;
			}
		}
	}
	return toolCalls;
}

/**
 * The first assistant text in a newest-first page of entries — the answer the
 * conversation finished on. Messages within an entry are scanned in reverse
 * (the newest first) to match the page order.
 */
export function firstAssistantText(items) {
	for (const entry of items ?? []) {
		for (const message of [...(entry?.model ?? [])].reverse()) {
			if (message?.role !== "assistant") continue;
			const blocks = Array.isArray(message.content) ? message.content : [];
			const joined = blocks
				.filter((block) => block?.type === "text" && typeof block.text === "string")
				.map((block) => block.text)
				.join("")
				.trim();
			if (joined) return joined;
		}
	}
	return "";
}
