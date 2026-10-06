/**
 * Integration test for the pi-durable sidecar bridge.
 *
 * Spawns the real sidecar, connects over the real Unix socket with the same
 * JSON-RPC peer the Go side uses, and drives it through the walking-skeleton
 * path: handshake → conversation.ensure → watch.start → turn.submit → tool
 * callbacks → shutdown.
 *
 * The tool-callback half is always exercised. The model turn runs only when
 * /data/agent/auth.json has a key for the test provider, so the suite stays
 * green offline; it prints SKIPPED loudly when credentials are missing.
 *
 * Usage: node test/bridge.test.mjs
 */
import net from "node:net";
import { execFile } from "node:child_process";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

import { createPeer } from "../src/rpc.js";

const run = promisify(execFile);
const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const AUTH_PATH = "/data/agent/auth.json";
const CATALOG_PATH = "/data/agent/models-store.json";
const PROVIDER_ID = "opencode-go";
const MODEL_ID = "deepseek-v4-flash";
const SESSION_HEADER = "x-opencode-session";

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

async function main() {
	const workspace = await mkdtemp(join(tmpdir(), "v1-sidecar-work-"));
	const stateDir = await mkdtemp(join(tmpdir(), "v1-sidecar-state-"));
	const socketPath = join(stateDir, "harness.sock");
	const database = join(stateDir, "harness.sqlite");
	const probePath = join(workspace, "probe.txt");
	const copyPath = join(workspace, "copy.txt");
	await writeFile(probePath, "hello from v1\n");

	let credentials = null;
	let catalog = null;
	try {
		credentials = JSON.parse(await readFile(AUTH_PATH, "utf8"))[PROVIDER_ID]?.key ?? null;
	} catch {
		credentials = null;
	}
	try {
		catalog = JSON.parse(await readFile(CATALOG_PATH, "utf8"))[PROVIDER_ID] ?? null;
	} catch {
		catalog = null;
	}
	const live = Boolean(credentials && catalog?.models?.length);

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

		const provider = {
			id: PROVIDER_ID,
			name: "opencode-go (sidecar test)",
			baseUrl: catalog?.baseUrl ?? catalog?.models?.[0]?.baseUrl ?? "https://opencode.ai/zen/go/v1",
			apiKey: credentials ?? "test-key-not-used-offline",
			api: "openai-completions",
			sessionHeader: SESSION_HEADER,
			models: catalog?.models ?? [],
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
				tools: ["read_file", "write_file", "run_command"],
			},
			{ timeoutMs: 30_000 },
		);
		check("conversation.ensure returns a conversationId", typeof ensured?.conversationId === "string" && ensured.conversationId.length > 0, JSON.stringify(ensured));
		check("conversation.ensure returns a keyed providerId", typeof ensured?.providerId === "string" && ensured.providerId.includes("#"), ensured?.providerId);
		check("conversation.ensure echoes the modelId", ensured?.modelId === MODEL_ID, ensured?.modelId);
		const conversationId = ensured.conversationId;

		await peer.call("watch.start", { subscriptionId: "sub-1", conversationId }, { timeoutMs: 30_000 });
		check("watch.start delivers an initial snapshot", events.some((event) => event.type === "snapshot"));

		let unknownCode = null;
		try {
			await peer.call("harness.nonsense", {}, { timeoutMs: 10_000 });
		} catch (error) {
			unknownCode = error.code;
		}
		check("an unknown method is a JSON-RPC method-not-found", unknownCode === -32601, String(unknownCode));

		if (!live) {
			console.log("\nmodel turn\n  SKIPPED (no credentials in /data/agent/auth.json for " + PROVIDER_ID + ")");
		} else {
			console.log("\nmodel turn");
			const submitted = await peer.call(
				"turn.submit",
				{
					conversationId,
					requestId: "test-turn-1",
					whenBusy: "reject",
					text:
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
		await rm(workspace, { recursive: true, force: true });
		await rm(stateDir, { recursive: true, force: true });
	}

	console.log(`\n${checks - failures}/${checks} checks passed${live ? "" : " (model turn skipped)"}`);
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

await main();
