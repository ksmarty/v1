#!/usr/bin/env node
/**
 * v1 pi-durable sidecar.
 *
 * Owns the model call, the turn loop and the durable transcript; calls back
 * into Go for every tool and every approval. Go spawns this process, waits for
 * the socket, and speaks newline-delimited JSON-RPC 2.0 over it.
 *
 * Environment (all set by Go, see internal/harness + internal/config):
 *   V1_SIDECAR_SOCKET  Unix socket to listen on (required)
 *   V1_HARNESS_DB      pi-durable sqlite store path (required)
 *   V1_SIDECAR_LOG     debug | info | warn | error (default info)
 */
import net from "node:net";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, unlink } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

import { Harness, createRegistry, defineExtension, section, watchEvents } from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { BACKGROUND_CONTEXT, withAbortSignal } from "@earendil-works/chord/context";

import { log } from "./log.js";
import { RpcError, createPeer } from "./rpc.js";
import { createModelStore, registerProvider } from "./provider.js";
import { buildApprovalHook, buildHostTools } from "./tools.js";

/** Bridge protocol revision; must equal `harness.ProtocolVersion` in Go. */
const PROTOCOL_VERSION = 1;
/** pi-durable storage schema the sidecar was built against. */
const SCHEMA_VERSION = 1;
/** node:sqlite (used by pi-durable's storage) needs a recent Node. */
const MIN_NODE = [22, 19, 0];

const require = createRequire(import.meta.url);

function readPackageVersion(name) {
	try {
		let dir = dirname(require.resolve(name));
		for (let depth = 0; depth < 5; depth += 1) {
			const candidate = join(dir, "package.json");
			if (existsSync(candidate)) return JSON.parse(readFileSync(candidate, "utf8")).version ?? "unknown";
			dir = dirname(dir);
		}
	} catch {
		// Fall through: the version is informational only.
	}
	return "unknown";
}

function assertNodeVersion() {
	const current = process.versions.node.split(".").map(Number);
	for (let index = 0; index < MIN_NODE.length; index += 1) {
		if ((current[index] ?? 0) > MIN_NODE[index]) return;
		if ((current[index] ?? 0) < MIN_NODE[index]) {
			throw new Error(
				`sidecar: Node >= ${MIN_NODE.join(".")} is required (node:sqlite), running ${process.versions.node}`,
			);
		}
	}
}

/**
 * The Go connection. Tools and hooks are built before Go connects, so they
 * capture this holder rather than a live peer.
 */
class Bridge {
	#peer = null;

	attach(peer) {
		this.#peer = peer;
	}

	detach() {
		this.#peer = null;
	}

	get connected() {
		return this.#peer !== null;
	}

	call(method, params, options) {
		const peer = this.#peer;
		if (!peer) return Promise.reject(new Error(`bridge: ${method} called before the host connected`));
		return peer.call(method, params, options);
	}

	notify(method, params) {
		const peer = this.#peer;
		if (!peer) return Promise.reject(new Error(`bridge: ${method} called before the host connected`));
		return peer.notify(method, params);
	}
}

/**
 * pi-ai rejects an unknown thinking level, so a value outside its documented
 * union is dropped rather than allowed to fail the turn. v1 has no thinking
 * setting of its own; the provider's default applies when this is undefined.
 */
const THINKING_LEVELS = new Set(["off", "minimal", "low", "medium", "high", "xhigh", "max"]);

function thinkingLevelOf(value) {
	return typeof value === "string" && THINKING_LEVELS.has(value) ? value : undefined;
}

class Sidecar {
	constructor({ socket, database, signal }) {
		this.socket = socket;
		this.database = database;
		this.signal = signal;
		this.bridge = new Bridge();
		this.models = createModelStore();
		this.registry = createRegistry();
		this.harness = null;
		this.ctx = null;
		/** subscriptionId → AgentEventStream */
		this.watches = new Map();
		/** v1SessionId → { conversation, providerId, modelId } */
		this.sessions = new Map();
		/** String(conversationId) → conversation */
		this.conversations = new Map();
		this.closed = false;
		this.closing = null;
		/** Set once Go has asked us to stop; makes the later disconnect graceful. */
		this.shutdownRequested = false;
	}

	async open() {
		this.ctx = withAbortSignal(this.signal, BACKGROUND_CONTEXT);
		// Tools arrive per turn from Go; the extension is installed up front for
		// its approval hook and cwd section.
		this.installHostTools([]);
		await mkdir(dirname(this.database), { recursive: true });
		const storage = await openNodeSqliteStorage(this.database);
		this.harness = await Harness.open(
			storage,
			{ models: this.models, registry: this.registry, settings: { toolExecution: "sequential" } },
			this.ctx,
		);
		this.harness.resume();
		log.info("harness opened", {
			database: this.database,
			tools: this.registry
				.snapshot()
				.tools()
				.map((entry) => entry.tool.name)
				.join(","),
		});
	}

	/* ---------------------------------------------------------------- *
	 * Methods
	 * ---------------------------------------------------------------- */

	async ready(params) {
		if (params?.protocolVersion !== undefined && params.protocolVersion !== PROTOCOL_VERSION) {
			throw new RpcError(
				-32600,
				`sidecar: protocol mismatch (host ${params.protocolVersion}, sidecar ${PROTOCOL_VERSION})`,
			);
		}
		log.info("handshake", { client: params?.client ?? "(unversioned)" });
		return {
			protocolVersion: PROTOCOL_VERSION,
			schemaVersion: SCHEMA_VERSION,
			nodeVersion: process.versions.node,
			piDurableVersion: readPackageVersion("@earendil-works/pi-durable"),
			database: this.database,
			pid: process.pid,
		};
	}

	/** `conversation.ensure` and `conversation.configure` share this path. */
	async ensure(params) {
		const { v1SessionId, conversationId, cwd, instructions, provider, model, thinkingLevel, toolDefs } = params ?? {};
		if (!v1SessionId && !conversationId) {
			throw new RpcError(-32602, "conversation.ensure: v1SessionId or conversationId is required");
		}
		if (!provider) throw new RpcError(-32602, "conversation.ensure: provider is required");

		const { providerId, modelId } = registerProvider(this.models, {
			...provider,
			modelId: model?.modelId ?? provider.modelId,
		});
		// Go owns the tool definitions, and it has already applied vision, the
		// user's disabled tools, plan mode and the project's MCP tools, so the
		// set installed here is exactly what the built-in loop would advertise.
		this.installHostTools(toolDefs);
		const change = compact({
			model: { provider: providerId, modelId },
			cwd,
			instructions,
			thinkingLevel: thinkingLevelOf(thinkingLevel),
			// Only an explicit tool list rebinds the conversation's tools: a partial
			// ensure (a rejoin that rebinds the model) must not empty them.
			...(Array.isArray(toolDefs) ? { tools: this.resolveTools(toolDefs.map((def) => def.name)) } : {}),
		});

		let conversation =
			(conversationId ? this.conversations.get(String(conversationId)) : undefined) ??
			(v1SessionId ? this.sessions.get(v1SessionId)?.conversation : undefined);
		if (!conversation && conversationId) {
			// Go remembers the conversation id across restarts, so this normally
			// hits. A miss means the durable store no longer has it (wiped, or a
			// different database): mint a fresh conversation and let the returned
			// id overwrite the stale one, rather than failing every turn.
			conversation = await this.harness.conversation(conversationId, this.ctx);
			if (!conversation) {
				log.warn("unknown conversation; creating a new one", { conversationId });
			}
		}

		if (conversation) {
			await conversation.configure(change, this.ctx);
		} else {
			conversation = await this.harness.createConversation({ ownership: { kind: "ownerless" }, agent: change }, this.ctx);
			log.info("conversation created", { v1SessionId, conversationId: String(conversation.id) });
		}

		if (v1SessionId) this.sessions.set(v1SessionId, { conversation, providerId, modelId });
		this.conversations.set(String(conversation.id), conversation);
		return { conversationId: String(conversation.id), providerId, modelId };
	}

	/**
	 * Install the host tools Go sent for a turn.
	 *
	 * The extension is replaced in place, so a conversation resumed later still
	 * resolves the same names, and Go re-sends the definitions on every ensure,
	 * so the schemas cannot drift from v1's Go definitions.
	 */
	installHostTools(defs) {
		// A partial ensure (a rejoin that only rebinds the model, or a configure)
		// carries no toolDefs. Treating that as "no tools" would silently strip
		// every tool the conversation had, so only an explicit list installs.
		if (!Array.isArray(defs)) return;
		const tools = buildHostTools(this.bridge, defs);
		this.registry.install(
			defineExtension({
				name: "v1-host-tools",
				tools,
				hooks: [buildApprovalHook(this.bridge)],
				// The cwd section is a placeholder: v1's real prompt blocks
				// (base prompt, memories, plan, tool guidance) arrive as the
				// conversation's `instructions` from Go.
				sections: [section("v1-cwd", (input) => input.env?.cwd, { tag: false })],
			}),
		);
		log.debug("host tools installed", { count: tools.length });
	}

	/**
	 * Map the tool names Go sends onto registry registrations. pi-durable stores
	 * names but an agent change carries registrations, and a name it does not know
	 * is a configuration error worth failing loudly rather than silently dropping.
	 */
	resolveTools(names) {
		if (!Array.isArray(names) || names.length === 0) return undefined;
		const available = new Map(this.registry.snapshot().tools().map((entry) => [entry.tool.name, entry.tool]));
		const missing = names.filter((name) => !available.has(name));
		if (missing.length) throw new RpcError(-32602, `conversation.ensure: unknown tools: ${missing.join(", ")}`);
		return names.map((name) => available.get(name));
	}

	async conversationFor(params) {
		const { conversationId, v1SessionId } = params ?? {};
		if (conversationId) {
			const cached = this.conversations.get(String(conversationId));
			if (cached) return cached;
			const conversation = await this.harness.conversation(conversationId, this.ctx);
			if (conversation) {
				this.conversations.set(String(conversation.id), conversation);
				return conversation;
			}
		}
		// Fall back to the v1 session the turn registered: Go only learns the id
		// pi-durable minted after an ensure, so a session-scoped call that arrives
		// without one still resolves.
		const session = v1SessionId ? this.sessions.get(v1SessionId) : undefined;
		if (session) return session.conversation;
		throw new RpcError(-32602, conversationId ? `unknown conversation ${conversationId}` : `unknown session ${v1SessionId}`);
	}

	async submit(params, whenBusy) {
		const { requestId, content } = params ?? {};
		if (!requestId) throw new RpcError(-32602, "turn.submit: requestId is required");
		// A string is a plain message; an array is content parts (text plus the
		// images and files the user attached).
		if (typeof content !== "string" && !Array.isArray(content)) {
			throw new RpcError(-32602, "turn.submit: content must be text or content parts");
		}
		if (typeof content === "string" && !content.length) {
			throw new RpcError(-32602, "turn.submit: text is required");
		}
		const conversation = await this.conversationFor(params);
		const submission = await conversation.submit(
			{ type: "input", requestId, content, whenBusy: params.whenBusy ?? whenBusy ?? "reject" },
			this.ctx,
		);
		return { conversationId: String(conversation.id), submissionId: String(submission.id) };
	}

	async abort(params) {
		const conversation = await this.conversationFor(params);
		await conversation.abort(this.ctx);
		return { ok: true };
	}

	async compact(params) {
		const conversation = await this.conversationFor(params);
		const taskId = await conversation.compact(params?.instructions, this.ctx);
		return { taskId: String(taskId) };
	}

	async reset(params) {
		const conversation = await this.conversationFor(params);
		await conversation.reset(params?.handoff, this.ctx);
		return { ok: true };
	}

	async entries(params) {
		const conversation = await this.conversationFor(params);
		const page = await conversation.entries({}, params?.limit ?? 200, params?.cursor, this.ctx);
		return { conversationId: String(conversation.id), items: page.items, cursor: page.cursor ?? null };
	}

	async usage() {
		return await this.harness.usage(this.ctx);
	}

	async inspect() {
		const inspection = await this.harness.inspect(this.ctx);
		return {
			scheduling: inspection.scheduling,
			conversations: [...this.conversations.keys()],
			watches: [...this.watches.keys()],
		};
	}

	async watchStart(params) {
		const subscriptionId = params?.subscriptionId;
		if (!subscriptionId) throw new RpcError(-32602, "watch.start: subscriptionId is required");
		if (this.watches.has(subscriptionId)) return { ok: true };
		const conversation = await this.conversationFor(params);
		const stream = await watchEvents(this.harness, conversation.id, this.ctx);
		const conversationKey = String(conversation.id);
		const send = (events) =>
			this.bridge.notify("event", { subscriptionId, conversationId: conversationKey, events });
		// The snapshot first, so Go can initialise state before deltas arrive.
		await send([stream.snapshot]);
		stream.start(async (events) => {
			await send(events);
		});
		this.watches.set(subscriptionId, stream);
		log.debug("watch started", { subscriptionId, conversationId: conversationKey });
		return { ok: true };
	}

	async watchStop(params) {
		const subscriptionId = params?.subscriptionId;
		const stream = this.watches.get(subscriptionId);
		if (stream) {
			this.watches.delete(subscriptionId);
			await stream.stop();
		}
		return { ok: true };
	}

	async shutdown() {
		if (this.closing) return await this.closing;
		this.closing = this.#close();
		return await this.closing;
	}

	async #close() {
		if (this.closed) return { ok: true };
		this.closed = true;
		for (const [subscriptionId, stream] of this.watches) {
			this.watches.delete(subscriptionId);
			try {
				await stream.stop();
			} catch (error) {
				log.warn("watch stop failed during shutdown", { subscriptionId, error: String(error) });
			}
		}
		if (this.harness) {
			try {
				await this.harness.close(this.ctx);
				log.info("harness closed");
			} catch (error) {
				log.warn("harness close failed", { error: String(error) });
			}
		}
		return { ok: true };
	}

	/** Route a request from Go. Unknown methods are a protocol error, not a crash. */
	async dispatch(method, params) {
		switch (method) {
			case "harness.ready":
				return await this.ready(params);
			case "harness.shutdown":
				// Go sends this, then closes the socket and our stdin. Answer
				// first (the reply is queued before we tear the socket down) and
				// let the caller trigger the actual process exit.
				this.shutdownRequested = true;
				return await this.shutdown();
			case "harness.inspect":
				return await this.inspect();
			case "conversation.ensure":
			case "conversation.configure":
				return await this.ensure(params);
			case "turn.submit":
				return await this.submit(params, "reject");
			case "turn.steer":
				return await this.submit(params, "steer");
			case "turn.followUp":
				return await this.submit(params, "followUp");
			case "turn.abort":
				return await this.abort(params);
			case "turn.compact":
				return await this.compact(params);
			case "turn.reset":
				return await this.reset(params);
			case "conversation.entries":
				return await this.entries(params);
			case "conversation.usage":
				return await this.usage(params);
			case "watch.start":
				return await this.watchStart(params);
			case "watch.stop":
				return await this.watchStop(params);
			default:
				throw new RpcError(-32601, `sidecar: unknown method ${method}`);
		}
	}
}

/* -------------------------------------------------------------------- *
 * Socket + process lifecycle
 * -------------------------------------------------------------------- */

/** True when something is already answering on the socket. */
function socketIsLive(path) {
	return new Promise((resolve) => {
		const probe = net.connect(path);
		const done = (live) => {
			probe.removeAllListeners();
			probe.destroy();
			resolve(live);
		};
		probe.once("connect", () => done(true));
		probe.once("error", () => done(false));
		probe.setTimeout(500, () => done(false));
	});
}

async function listen(path) {
	await mkdir(dirname(path), { recursive: true });
	if (existsSync(path)) {
		// Only one process may own the durable store; a live socket means a
		// running sidecar, not a leftover file.
		if (await socketIsLive(path)) {
			throw new Error(`sidecar: another sidecar is already listening on ${path}`);
		}
		await unlink(path);
	}
	const server = net.createServer();
	await new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(path, () => {
			server.removeListener("error", reject);
			resolve(undefined);
		});
	});
	return server;
}

function fail(message) {
	log.error(message);
	process.exit(1);
}

/**
 * Drop `undefined` fields. pi-durable validates every value it stores as strict
 * JSON, so an explicitly-undefined agent field is an error, not "unchanged".
 */
function compact(value) {
	return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined));
}

async function main() {
	const socketPath = process.env.V1_SIDECAR_SOCKET;
	const database = process.env.V1_HARNESS_DB;
	if (!socketPath) fail("V1_SIDECAR_SOCKET is required");
	if (!database) fail("V1_HARNESS_DB is required");
	try {
		assertNodeVersion();
	} catch (error) {
		fail(error.message);
	}

	const controller = new AbortController();
	const sidecar = new Sidecar({ socket: socketPath, database, signal: controller.signal });

	let server = null;
	let peer = null;
	let finish;
	const finished = new Promise((resolve) => {
		finish = resolve;
	});
	let exiting = false;
	let exitCode = 0;

	/** Stop accepting work, close the store, and let main() return. */
	async function shutdown(reason, code = 0) {
		if (exiting) return;
		exiting = true;
		exitCode = code;
		log.info("shutting down", { reason });
		controller.abort();
		try {
			await sidecar.shutdown();
		} catch (error) {
			log.warn("shutdown failed", { error: String(error) });
		}
		if (server) await new Promise((resolve) => server.close(resolve));
		if (peer) await peer.close();
		try {
			if (existsSync(socketPath)) await unlink(socketPath);
		} catch {
			// The socket is unlinked by the OS at worst.
		}
		finish(undefined);
	}

	await sidecar.open();
	server = await listen(socketPath);
	log.info("listening", { socket: socketPath, node: process.versions.node, pid: process.pid });

	server.on("connection", (socket) => {
		if (peer && !peer.closed) {
			log.warn("rejecting a second host connection");
			socket.destroy();
			return;
		}
		peer = createPeer(socket, {
			handle: async (method, params) => {
				try {
					return await sidecar.dispatch(method, params);
				} catch (error) {
					// Only our own RpcErrors carry an intentional message; anything else
					// is a bug in the bridge, so keep the stack in the sidecar log where
					// Go can surface it (the JSON-RPC reply only carries the message).
					if (!(error instanceof RpcError)) log.error("dispatch failed", { method, error: error?.stack ?? String(error) });
					throw error;
				}
			},
			onNotify: async (method, params) => {
				log.debug("notification from host", { method, params });
			},
			onClose: (error) => {
				sidecar.bridge.detach();
				if (exiting) return;
				if (sidecar.shutdownRequested || !error) {
					void shutdown("host disconnected after shutdown request", 0);
					return;
				}
				// Go is gone without asking: exit so the supervisor can restart us.
				log.error("host connection lost", { error: error.message });
				void shutdown("host connection lost", 1);
			},
		});
		sidecar.bridge.attach(peer);
		log.info("host connected");
	});

	// Go closes our stdin as part of its graceful shutdown sequence.
	process.stdin.on("end", () => void shutdown("stdin closed"));
	process.stdin.on("close", () => void shutdown("stdin closed"));
	process.stdin.resume();

	process.on("SIGTERM", () => void shutdown("SIGTERM"));
	process.on("SIGINT", () => void shutdown("SIGINT"));
	process.on("unhandledRejection", (error) => {
		log.error("unhandled rejection", { error: error?.stack ?? String(error) });
		void shutdown("unhandled rejection", 1);
	});
	process.on("uncaughtException", (error) => {
		log.error("uncaught exception", { error: error?.stack ?? String(error) });
		void shutdown("uncaught exception", 1);
	});

	await finished;
	return exitCode;
}

main().then(
	(code) => {
		// By now the store is closed, the socket is unlinked and every pending
		// reply is flushed. Exit explicitly: stdin is resumed, so the event loop
		// would otherwise keep the process alive forever and Go's supervisor
		// would have to escalate to SIGTERM on every shutdown.
		process.exit(code);
	},
	(error) => {
		log.error("sidecar failed to start", { error: error?.stack ?? String(error) });
		process.exit(1);
	},
);
