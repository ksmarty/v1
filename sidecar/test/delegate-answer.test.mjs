import assert from "node:assert/strict";
import test from "node:test";

import { readDelegateAnswer } from "../src/delegate_answer.js";
import { buildContinuationHook } from "../src/tools.js";

function user(text) {
	return { model: [{ role: "user", content: [{ type: "text", text }] }] };
}
function assistant(text, tools = 0) {
	return {
		model: [{
			role: "assistant",
			content: [
				...(text ? [{ type: "text", text }] : []),
				...Array.from({ length: tools }, () => ({ type: "toolCall" })),
			],
		}],
	};
}
function conversation(items) {
	const calls = [];
	return {
		id: "child-1",
		calls,
		async entries(_query, limit, cursor) {
			const start = cursor?.start ?? 0;
			calls.push(start);
			const page = items.slice(start, start + limit);
			return { items: page, ...(start + limit < items.length ? { next: { start: start + limit } } : {}) };
		},
	};
}

test("delegate answers and tool counts include entries older than the first page", async () => {
	const items = [
		...Array.from({ length: 40 }, (_, i) => user(`recent ${i}`)),
		assistant("final answer"),
		assistant("", 2),
		user("original task"),
	];
	const child = conversation(items);
	const result = await readDelegateAnswer(child, 20000, undefined);
	assert.equal(result.conversationId, "child-1");
	assert.equal(result.text, "final answer");
	assert.equal(result.toolCalls, 2);
	assert.deepEqual(child.calls, [0, 40]);
});

test("delegate reader counts older tools even when the newest page has an answer", async () => {
	const child = conversation([assistant("newest answer", 1), ...Array.from({ length: 40 }, () => user("work")), assistant("earlier", 2)]);
	const result = await readDelegateAnswer(child, 20000, undefined);
	assert.equal(result.text, "newest answer");
	assert.equal(result.toolCalls, 3);
	assert.deepEqual(child.calls, [0, 40]);
});

test("delegate answer truncation remains unchanged after pagination", async () => {
	const child = conversation([assistant("abcdef"), ...Array.from({ length: 41 }, () => user("old"))]);
	const result = await readDelegateAnswer(child, 3, undefined);
	assert.equal(result.text, "abc\n… (truncated)");
});

test("continuation budget resets after a visible answer so the next turn gets all retries", async () => {
	const { handlers } = buildContinuationHook();
	const api = { conversationId: "conv-continuation-reset" };
	const empty = { content: [], stopReason: "stop" };
	const answered = { content: [{ type: "text", text: "done" }], stopReason: "stop" };

	assert.ok(await handlers.onYield(empty, api, undefined));
	assert.ok(await handlers.onYield(empty, api, undefined));
	assert.equal(await handlers.onYield(answered, api, undefined), undefined);

	// A follow-up turn in the same conversation must have its own three-attempt budget.
	assert.ok(await handlers.onYield(empty, api, undefined));
	assert.ok(await handlers.onYield(empty, api, undefined));
	assert.ok(await handlers.onYield(empty, api, undefined));
	assert.equal(await handlers.onYield(empty, api, undefined), undefined);
});
