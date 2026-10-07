/**
 * Integration test for the pi-durable sidecar bridge.
 *
 * Spawns the real sidecar, connects over the real Unix socket with the same
 * JSON-RPC peer the Go side uses, and drives it through the walking-skeleton
 * path: handshake → conversation.ensure → watch.start → turn.submit → tool
 * callbacks → shutdown.
 *
 * The tool-callback half is always exercised. The model turn runs against a
 * scripted fake endpoint (startFakeProvider) rather than a real model: a real
 * model makes every assertion about the turn a coin toss — whether it called
 * write_file at all, whether it streamed text before the tool call, whether it
 * finished inside the timeout. The fake answers deterministically, so the
 * assertions test the bridge, and it needs no credentials, so the turn is no
 * longer skipped offline.
 *
 * Usage: node test/bridge.test.mjs
 */
import net from "node:net";
import http from "node:http";
import { execFile } from "node:child_process";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { AsyncLocalStorage } from "node:async_hooks";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

import { createPeer } from "../src/rpc.js";
import { bindToolToConversation, loadExtensions, toolNames } from "../src/extensions.js";
import { endpointHeaders } from "../src/provider.js";
import { buildHostTools } from "../src/tools.js";

const run = promisify(execFile);
const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const PROVIDER_ID = "opencode-go";
const MODEL_ID = "deepseek-v4-flash";
const SESSION_HEADER = "x-opencode-session";
// opencode wants its client attribution alongside the routing header, so the
// provider descriptor has to carry more than one header.
const STATIC_HEADERS = { "x-opencode-client": "v1" };

let checks = 0;
let failures = 0;

function check(name, ok, detail) {
	checks += 1;
	if (ok) {
		console.log(`  ok   ${name}`);
		return;
	}
	failures += 1;
	console.log(`  FAIL ${name}${detail ? ` — ${detail}` : ""}`);
}

async function connect(socketPath, timeoutMs = 10_000) {
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		if (existsSync(socketPath)) {
			const socket = net.connect(socketPath);
			try {
				await new Promise((resolve, reject) => {
					socket.once("connect", resolve);
					socket.once("error", reject);
				});
				return socket;
			} catch {
				socket.destroy();
			}
		}
		if (Date.now() > deadline) throw new Error(`socket ${socketPath} never appeared`);
		await new Promise((resolve) => setTimeout(resolve, 50));
	}
}

/**
 * How long to wait between streamed chunks.
 *
 * pi-durable publishes the in-flight message on a coalescing boundary, so
 * deltas that arrive closer together than that boundary land in one batch: the
 * watch then sees a single message_start and no message_update at all, and the
 * delta channel this check exists to cover goes untested. 300ms is comfortably
 * above the boundary — 25ms collapsed every delta into one batch.
 */
const CHUNK_INTERVAL_MS = 300;

/** One OpenAI-compatible streaming chunk. */
function sseChunk(delta, finishReason) {
	return {
		id: "chatcmpl-sidecar-test",
		object: "chat.completion.chunk",
		created: 1_700_000_000,
		model: MODEL_ID,
		choices: [{ index: 0, delta, finish_reason: finishReason ?? null }],
	};
}

function textDelta(content) {
	return sseChunk({ role: "assistant", content });
}

function toolCallDelta(index, name, args) {
	return sseChunk({
		tool_calls: [
			{ index, id: `call_${index + 1}`, type: "function", function: { name, arguments: JSON.stringify(args) } },
		],
	});
}

function finishDelta(reason) {
	return sseChunk({}, reason);
}

/** The client asks for usage in the stream (stream_options.include_usage). */
function usageChunk() {
	return {
		id: "chatcmpl-sidecar-test",
		object: "chat.completion.chunk",
		created: 1_700_000_000,
		model: MODEL_ID,
		choices: [],
		usage: { prompt_tokens: 42, completion_tokens: 7, total_tokens: 49 },
	};
}

/**
 * The next scripted reply, chosen from how many tool results the transcript
 * already holds.
 *
 * Echoing the read_file result back as the write_file body is deliberate: the
 * round-trip assertion can then only pass if the tool result really reached the
 * provider, which is the part a real model made unverifiable.
 */
function scriptedReply(messages) {
	const results = messages.filter((message) => message.role === "tool");
	if (results.length === 0) {
		return [
			textDelta("Reading the file "),
			textDelta("first."),
			toolCallDelta(0, "read_file", { path: "probe.txt" }),
			finishDelta("tool_calls"),
		];
	}
	if (results.length === 1) {
		const content = String(results[results.length - 1]?.content ?? "");
		return [toolCallDelta(1, "write_file", { path: "copy.txt", content }), finishDelta("tool_calls")];
	}
	if (results.length === 2) {
		return [toolCallDelta(2, "run_command", { command: "echo tool-round-trip-ok" }), finishDelta("tool_calls")];
	}
	return [textDelta("Done."), finishDelta("stop"), usageChunk()];
}

/**
 * A scripted OpenAI-compatible endpoint, so the model turn is deterministic.
 *
 * @returns {Promise<{ url: string, requests: object[], close: () => Promise<void> }>}
 */
async function startFakeProvider() {
	const requests = [];
	const server = http.createServer((request, response) => {
		let body = "";
		request.on("data", (chunk) => {
			body += chunk;
		});
		request.on("end", async () => {
			const payload = JSON.parse(body || "{}");
			requests.push({ url: request.url, headers: request.headers, body: payload });
			response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
			try {
				// Paced on purpose. pi-durable emits a message_update only when the
				// in-flight message changed since the previous event batch, so a
				// response written in one burst collapses into a single message_start
				// and the delta channel — the thing this check exists to cover — never
				// gets exercised. A real model is slow enough to avoid that by accident,
				// which is exactly the accident that made this suite flaky.
				for (const chunk of scriptedReply(payload.messages ?? [])) {
					response.write(`data: ${JSON.stringify(chunk)}\n\n`);
					await new Promise((resolve) => setTimeout(resolve, CHUNK_INTERVAL_MS));
				}
				response.write("data: [DONE]\n\n");
			} finally {
				response.end();
			}
		});
	});
	await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
	const { port } = server.address();
	return {
		url: `http://127.0.0.1:${port}/v1`,
		requests,
		close: () => new Promise((resolve) => server.close(resolve)),
	};
}

async function main() {
	const workspace = await mkdtemp(join(tmpdir(), "v1-sidecar-work-"));
	const stateDir = await mkdtemp(join(tmpdir(), "v1-sidecar-state-"));
	const socketPath = join(stateDir, "harness.sock");
	const database = join(stateDir, "harness.sqlite");
	const probePath = join(workspace, "probe.txt");
	const copyPath = join(workspace, "copy.txt");
	await writeFile(probePath, "hello from v1\n");

	const fake = await startFakeProvider();

	const child = spawn(process.execPath, [join(ROOT, "src", "host.js")], {
		env: {
			...process.env,
			V1_SIDECAR_SOCKET: socketPath,
			V1_HARNESS_DB: database,
			V1_SIDECAR_LOG: process.env.V1_SIDECAR_LOG ?? "info",
		},
		stdio: ["pipe", "inherit", "inherit"],
	});
	let exitCode = null;
	child.on("exit", (code) => {
		exitCode = code;
	});

	/** Every event batch the sidecar pushed, flattened. */
	const events = [];
	let authorizations = 0;
	const toolCalls = [];

	const socket = await connect(socketPath);
	const peer = createPeer(socket, {
		handle: async (method, params) => {
			if (method === "tool.authorize") {
				authorizations += 1;
				return { block: null };
			}
			if (method !== "tool.call") throw new Error(`test host: unknown method ${method}`);
			toolCalls.push(params.tool);
			switch (params.tool) {
				case "read_file": {
					const path = String(params.arguments?.path ?? "");
					const resolved = path.startsWith("/") ? path : join(workspace, path);
					// The path guard that will live in Go.
					if (!resolved.startsWith(workspace)) return { text: `path escapes workspace: ${path}`, isError: true };
					try {
						return { text: await readFile(resolved, "utf8"), isError: false };
					} catch (error) {
						return { text: `read failed: ${error.message}`, isError: true };
					}
				}
				case "write_file": {
					const path = String(params.arguments?.path ?? "");
					const resolved = path.startsWith("/") ? path : join(workspace, path);
					if (!resolved.startsWith(workspace)) return { text: `path escapes workspace: ${path}`, isError: true };
					await writeFile(resolved, String(params.arguments?.content ?? ""));
					return { text: `wrote ${resolved}`, isError: false };
				}
				case "run_command": {
					try {
						const { stdout, stderr } = await run("/bin/sh", ["-c", String(params.arguments?.command ?? "")], {
							cwd: workspace,
							timeout: 30_000,
						});
						return { text: `${stdout}${stderr}`.trim() || "(no output)", isError: false };
					} catch (error) {
						return { text: `${error.stdout ?? ""}${error.stderr ?? ""}${error.message}`, isError: true };
					}
				}
				default:
					return { text: `unknown tool ${params.tool}`, isError: true };
			}
		},
		onNotify: async (method, params) => {
			if (method !== "event") return;
			for (const event of params.events ?? []) events.push(event);
		},
	});

	try {
		console.log("\nbridge surface");

		const ready = await peer.call("harness.ready", { protocolVersion: 1, client: "bridge.test.mjs" }, { timeoutMs: 10_000 });
		check("harness.ready reports protocol 1", ready?.protocolVersion === 1, JSON.stringify(ready));
		check("harness.ready reports schema 1", ready?.schemaVersion === 1);
		check("harness.ready reports the store path", ready?.database === database);
		check("harness.ready reports a pid", typeof ready?.pid === "number" && ready.pid > 0);
		check("harness.ready reports pi-durable 1.0.4", ready?.piDurableVersion === "1.0.4", ready?.piDurableVersion);
		check("harness.ready reports the node version", typeof ready?.nodeVersion === "string", ready?.nodeVersion);

		// The provider's static headers ride along on every request, and the
		// routing header keeps its value. A descriptor that could express only one
		// header is what let opencode's second header go missing.
		const merged = endpointHeaders({ id: PROVIDER_ID, sessionHeader: SESSION_HEADER, headers: STATIC_HEADERS }, { sessionId: "sess-7" });
		check("static headers are sent", merged["x-opencode-client"] === "v1", JSON.stringify(merged));
		check("the routing header carries the session id", merged[SESSION_HEADER] === "sess-7", JSON.stringify(merged));
		check(
			"static headers are sent even with no session",
			endpointHeaders({ id: PROVIDER_ID, headers: STATIC_HEADERS }, {})["x-opencode-client"] === "v1",
		);
		check("an endpoint needing no headers gets none", Object.keys(endpointHeaders({ id: PROVIDER_ID }, {})).length === 0);
		check(
			"caller headers win over static ones",
			endpointHeaders({ id: PROVIDER_ID, headers: STATIC_HEADERS }, { headers: { "x-opencode-client": "caller" } })["x-opencode-client"] === "caller",
		);
		check(
			"the routing header falls back to the provider id",
			endpointHeaders({ id: PROVIDER_ID, sessionHeader: SESSION_HEADER }, {})[SESSION_HEADER] === PROVIDER_ID,
		);

		const provider = {
			id: PROVIDER_ID,
			name: "opencode-go (sidecar test)",
			baseUrl: fake.url,
			apiKey: "test-key",
			api: "openai-completions",
			sessionHeader: SESSION_HEADER,
			headers: STATIC_HEADERS,
			models: [{ id: MODEL_ID, name: "Scripted test model", contextWindow: 128_000, maxTokens: 8192 }],
		};

		const ensured = await peer.call(
			"conversation.ensure",
			{
				v1SessionId: "test-session-1",
				cwd: workspace,
				instructions: "You are a terse test agent.",
				provider,
				model: { provider: PROVIDER_ID, modelId: MODEL_ID },
				thinkingLevel: "low",
				// Plain JSON Schema, exactly as Go sends it: the sidecar must accept
				// v1's own definitions rather than rebuild them.
				toolDefs: [
					{
						name: "read_file",
						description: "Read a UTF-8 text file from the project.",
						parameters: {
							type: "object",
							properties: { path: { type: "string", description: "Path to the file" } },
							required: ["path"],
						},
					},
					{
						name: "write_file",
						description: "Write a UTF-8 text file in the project.",
						parameters: {
							type: "object",
							properties: {
								path: { type: "string", description: "Path to the file" },
								content: { type: "string", description: "Full file contents" },
							},
							required: ["path", "content"],
						},
					},
					{
						name: "run_command",
						description: "Run a shell command in the project directory.",
						parameters: {
							type: "object",
							properties: { command: { type: "string", description: "Command line" } },
							required: ["command"],
						},
					},
				],
			},
			{ timeoutMs: 30_000 },
		);
		check("conversation.ensure returns a conversationId", typeof ensured?.conversationId === "string" && ensured.conversationId.length > 0, JSON.stringify(ensured));
		check("conversation.ensure returns a keyed providerId", typeof ensured?.providerId === "string" && ensured.providerId.includes("#"), ensured?.providerId);
		check("conversation.ensure echoes the modelId", ensured?.modelId === MODEL_ID, ensured?.modelId);
		const conversationId = ensured.conversationId;

		// Go remembers the id pi-durable minted and sends it back on every later
		// turn, so ensure must rejoin that conversation rather than mint another
		// one — otherwise a restarted sidecar forks the session and the model
		// silently loses its history.
		const rejoined = await peer.call(
			"conversation.ensure",
			{
				v1SessionId: "test-session-1",
				conversationId,
				provider,
				model: { provider: PROVIDER_ID, modelId: MODEL_ID },
			},
			{ timeoutMs: 30_000 },
		);
		check("conversation.ensure rejoins the remembered conversation", rejoined?.conversationId === conversationId, JSON.stringify(rejoined));

		// A stale id (wiped store, different database) must not fail every turn:
		// the sidecar mints a fresh conversation and Go overwrites its record.
		const recovered = await peer.call(
			"conversation.ensure",
			{
				v1SessionId: "test-session-stale",
				conversationId: "999999",
				provider,
				model: { provider: PROVIDER_ID, modelId: MODEL_ID },
			},
			{ timeoutMs: 30_000 },
		);
		check("conversation.ensure recovers from a stale conversationId", typeof recovered?.conversationId === "string" && recovered.conversationId.length > 0 && recovered.conversationId !== "999999", JSON.stringify(recovered));

		await peer.call("watch.start", { subscriptionId: "sub-1", conversationId }, { timeoutMs: 30_000 });
		check("watch.start delivers an initial snapshot", events.some((event) => event.type === "snapshot"));

		let unknownCode = null;
		try {
			await peer.call("harness.nonsense", {}, { timeoutMs: 10_000 });
		} catch (error) {
			unknownCode = error.code;
		}
		check("an unknown method is a JSON-RPC method-not-found", unknownCode === -32601, String(unknownCode));

		{
			console.log("\nmodel turn");
			const submitted = await peer.call(
				"turn.submit",
				{
					conversationId,
					requestId: "test-turn-1",
					whenBusy: "reject",
					content:
						"Do exactly three things, in order: (1) read probe.txt, " +
						"(2) write its exact contents to copy.txt, " +
						"(3) run the shell command `echo tool-round-trip-ok`. " +
						"Then reply with one short sentence.",
				},
				{ timeoutMs: 60_000 },
			);
			check("turn.submit returns a submissionId", typeof submitted?.submissionId === "string" && submitted.submissionId.length > 0, JSON.stringify(submitted));

			const settled = await waitFor(
				() =>
					events.some(
						(event) =>
							event.type === "submission" &&
							["done", "unanswered"].includes(event.record?.status),
					),
				240_000,
			);
			check("the turn settled", settled, "no settled submission within 240s");

			// The static and routing headers have to reach the provider, not merely be
			// computed: an endpoint that rejects unroutable requests fails the turn.
			const first = fake.requests[0] ?? {};
			check("the turn reached the provider's chat-completions endpoint", first.url === "/v1/chat/completions", first.url);
			check("the provider received the static headers", first.headers?.["x-opencode-client"] === "v1", String(first.headers?.["x-opencode-client"]));
			check("the provider received the routing header", typeof first.headers?.[SESSION_HEADER] === "string" && first.headers[SESSION_HEADER].length > 0, String(first.headers?.[SESSION_HEADER]));

			const types = new Set(events.map((event) => event.type));
			if (process.env.V1_TEST_DUMP) dumpEvents(events);
			// Both delta kinds flow through the same stream; the Go SSE mapping has to
			// handle each, so assert the channel works without pinning the model to a
			// particular mix of text and reasoning.
			const deltas = new Set(
				events
					.filter((event) => event.type === "message_update")
					.flatMap((event) => event.changes ?? [])
					.map((change) => change.type)
					.filter((type) => type === "text_delta" || type === "thinking_delta"),
			);
			check("message deltas streamed", deltas.size > 0, [...deltas].join(",") || "none");
			check("tool executions started", events.some((event) => event.type === "tool_execution_start"));
			check("tool executions ended", events.some((event) => event.type === "tool_execution_end"));
			check("assistant messages were committed", events.some((event) => event.type === "message_end"));
			check("run_end was reported", types.has("run_end"), [...types].join(","));
			check("usage was reported", types.has("usage_changed") || types.has("snapshot"));
			check("every tool call was authorized", authorizations === toolCalls.length, `${authorizations} auth vs ${toolCalls.length} calls`);
			check("the model called read_file", toolCalls.includes("read_file"), toolCalls.join(","));
			check("the model called write_file", toolCalls.includes("write_file"), toolCalls.join(","));

			const copied = await readFile(copyPath, "utf8").catch(() => null);
			check("the write_file round trip reached the host filesystem", copied === "hello from v1\n", JSON.stringify(copied));

			const entries = await peer.call("conversation.entries", { conversationId, limit: 100 }, { timeoutMs: 30_000 });
			check("the transcript persisted entries", (entries?.items?.length ?? 0) >= 2, `${entries?.items?.length} entries`);
			const kinds = new Set((entries?.items ?? []).map((entry) => entry.kind));
			check("the transcript holds a user entry", kinds.has("pi.user"), [...kinds].join(","));
			check("the transcript holds an assistant entry", kinds.has("pi.assistant"), [...kinds].join(","));
			check("the transcript holds tool results", kinds.has("pi.tool-result"), [...kinds].join(","));

			const usage = await peer.call("conversation.usage", {}, { timeoutMs: 30_000 });
			check("usage is reported per model", Object.keys(usage?.models ?? {}).length > 0, JSON.stringify(usage));
		}

		console.log("\nlifecycle");
		await peer.call("harness.shutdown", { reason: "test finished" }, { timeoutMs: 20_000 });
		await peer.close();
		const exited = await waitFor(() => exitCode !== null, 20_000);
		check("the sidecar exited after shutdown", exited, `exitCode=${exitCode}`);
		check("shutdown exited cleanly (code 0)", exitCode === 0, `exitCode=${exitCode}`);
		check("the socket was removed", !existsSync(socketPath));
		check("the durable store was written", existsSync(database));
	} catch (error) {
		failures += 1;
		console.log(`  FAIL a bridge call threw — ${error?.stack ?? error}`);
	} finally {
		if (exitCode === null) child.kill("SIGKILL");
		await peer.close().catch(() => undefined);
		await fake.close();
		await rm(workspace, { recursive: true, force: true });
		await rm(stateDir, { recursive: true, force: true });
	}

	console.log(`\n${checks - failures}/${checks} checks passed`);
	if (failures > 0) process.exitCode = 1;
}

/** Print one sample of every event type and every change type (V1_TEST_DUMP=1). */
function dumpEvents(events) {
	console.log("\nevent vocabulary (V1_TEST_DUMP)");
	const samples = new Map();
	for (const event of events) if (!samples.has(event.type)) samples.set(event.type, event);
	for (const [type, sample] of samples) {
		console.log(`  ${type}: ${JSON.stringify(sample).slice(0, 260)}`);
	}
	const changeTypes = new Set();
	for (const event of events) for (const change of event.changes ?? []) changeTypes.add(change.type);
	console.log(`  change types: ${[...changeTypes].join(", ")}`);
}

async function waitFor(predicate, timeoutMs) {
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		if (predicate()) return true;
		if (Date.now() > deadline) return false;
		await new Promise((resolve) => setTimeout(resolve, 100));
	}
}

/**
 * The Go tool result → pi-durable tool result mapping. Runs without a sidecar
 * or a model: it exercises the proxy's translation directly, including the
 * image block a screenshot_app result carries.
 */
/**
 * Extensions are loaded from disk, but whether one is enabled is v1's decision,
 * pushed along with the reload. A disabled extension must not be imported at
 * all — skipping only the install would still have run its factory.
 */
async function checkExtensionLoading() {
	const root = await mkdtemp(join(tmpdir(), "v1-extensions-"));
	const write = async (id, source) => {
		await mkdir(join(root, id), { recursive: true });
		await writeFile(join(root, id, "index.js"), source, "utf8");
	};
	await write("alpha", 'export default () => ({ name: "alpha", tools: [{ name: "alpha_tool" }] });\n');
	await write("beta", 'export default () => ({ name: "beta", tools: [{ name: "beta_tool" }] });\n');
	await write("broken", "export default (pi) => ({\n");

	// Only the enabled id is imported.
	const filtered = await loadExtensions(root, {}, new Set(["alpha"]));
	check(
		"extensions: only the enabled one is loaded",
		filtered.extensions.length === 1 && filtered.extensions[0].id === "alpha",
		JSON.stringify(filtered.extensions.map((entry) => entry.id)),
	);
	check(
		"extensions: a loaded extension keeps its tools",
		toolNames(filtered.extensions[0].extension)[0] === "alpha_tool",
	);
	check(
		"extensions: a disabled extension contributes nothing",
		filtered.errors.length === 0,
		JSON.stringify(filtered.errors),
	);

	// Without a filter everything loads, and a broken file is reported rather
	// than fatal: one bad extension must not take the sidecar down.
	const all = await loadExtensions(root, {}, null);
	check(
		"extensions: without a filter everything loads",
		all.extensions.length === 2,
		JSON.stringify(all.extensions.map((entry) => entry.id)),
	);
	check(
		"extensions: a broken module is reported, not fatal",
		all.errors.some((entry) => entry.id === "broken"),
		JSON.stringify(all.errors),
	);

	// A missing directory is simply "no extensions".
	const missing = await loadExtensions(join(root, "absent"), {}, null);
	check(
		"extensions: a missing directory is not an error",
		missing.extensions.length === 0 && missing.errors.length === 0,
	);

	await rm(root, { recursive: true, force: true });
}

/**
 * A tool must see the conversation it was offered in, even while another
 * conversation runs a turn at the same time. That is why the binding travels in
 * async context rather than on a field of the host.
 */
async function checkConversationBinding() {
	const storage = new AsyncLocalStorage();
	const seen = [];
	const tool = {
		name: "whoami",
		async execute() {
			// Yield, so the two conversations really do interleave.
			await new Promise((resolve) => setTimeout(resolve, 5));
			seen.push(storage.getStore());
			return {};
		},
	};

	const a = bindToolToConversation(tool, { conversationId: "conv-a" }, storage);
	const b = bindToolToConversation(tool, { conversationId: "conv-b" }, storage);
	await Promise.all([a.execute({}, {}, {}), b.execute({}, {}, {}), a.execute({}, {}, {})]);
	check(
		"extensions: a tool sees its own conversation",
		seen[0] === "conv-a" && seen[1] === "conv-b" && seen[2] === "conv-a",
		JSON.stringify(seen),
	);
	check(
		"extensions: binding keeps the tool's own fields",
		a.name === "whoami" && typeof a.execute === "function",
	);

	// The id is read at call time, so a box filled in after binding still works —
	// which is what a conversation created during the ensure needs.
	const late = { conversationId: null };
	const lateTool = bindToolToConversation(tool, late, storage);
	late.conversationId = "conv-late";
	seen.length = 0;
	await lateTool.execute({}, {}, {});
	check("extensions: a late-bound conversation is used", seen[0] === "conv-late", JSON.stringify(seen));

	seen.length = 0;
	await bindToolToConversation(tool, { conversationId: null }, storage).execute({}, {}, {});
	check("extensions: an unresolved conversation runs unbound", seen[0] === undefined, JSON.stringify(seen));
}

async function checkToolResultMapping() {
	const bridge = {
		call: async () => ({
			text: "captured the app preview",
			isError: false,
			details: { ok: true, bytes: 8 },
			images: [{ data: "iVBORw0KGgo=", mimeType: "image/png" }],
		}),
	};
	const [tool] = buildHostTools(bridge, [
		{
			name: "screenshot_app",
			description: "Capture a screenshot of the app preview.",
			parameters: { type: "object", properties: {} },
		},
	]);
	const result = await tool.execute({}, { callId: "call-1", conversationId: "conv-1" });
	check("tool result: text block", result.content[0]?.type === "text" && result.content[0].text === "captured the app preview");
	check("tool result: image block", result.content[1]?.type === "image" && result.content[1].data === "iVBORw0KGgo=", JSON.stringify(result.content[1]));
	check("tool result: image mime type", result.content[1]?.mimeType === "image/png");
	check("tool result: details pass through", result.details?.ok === true);
	check("tool result: not an error", result.isError === false);

	// A text-only result must not gain an empty image block, which a provider
	// would reject.
	const textOnly = buildHostTools({ call: async () => ({ text: "ok", isError: false }) }, [
		{ name: "read_file", description: "Read a file.", parameters: { type: "object", properties: {} } },
	])[0];
	const plain = await textOnly.execute({}, { callId: "call-2", conversationId: "conv-1" });
	check("tool result: no image block for a text result", plain.content.length === 1 && plain.content[0].type === "text");
}

/**
 * turn.submit accepts both a plain message and content parts. The Go side sends
 * parts for a turn with attachments, so the sidecar must forward them rather
 * than expect a string.
 */
async function checkSubmitContentValidation(peer, conversationId) {
	const rejected = await peer
		.call("turn.submit", { conversationId, requestId: "bad-1", content: 42 })
		.then(() => null)
		.catch((err) => err);
	check("turn.submit rejects non-string, non-array content", Boolean(rejected), String(rejected));
}

await checkExtensionLoading();
await checkConversationBinding();
await checkToolResultMapping();
await main();
