import { countToolCalls, firstAssistantText } from "./entries.js";

/** Reads a delegate's newest final text and tool usage across all entry pages. */
export async function readDelegateAnswer(conversation, maxChars, context) {
	let cursor;
	let toolCalls = 0;
	let text = "";
	for (;;) {
		const page = await conversation.entries({}, 40, cursor, context);
		const items = page?.items ?? [];
		toolCalls += countToolCalls(items);
		// entries() is newest-first: only the first non-empty assistant text is
		// the final answer, but older pages still contain earlier tool rounds.
		if (!text) text = firstAssistantText(items);
		if (page?.next == null) break;
		cursor = page.next;
	}
	const limit = Number.isInteger(maxChars) && maxChars > 0 ? maxChars : 20000;
	const truncated = text.length > limit;
	return {
		conversationId: String(conversation.id),
		text: truncated ? `${text.slice(0, limit)}\n… (truncated)` : text,
		toolCalls,
	};
}
