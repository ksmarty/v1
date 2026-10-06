/**
 * JSON-RPC 2.0 peer over a newline-delimited stream (the Unix socket to Go).
 *
 * This is the sidecar half of the bridge described in the plan: Go sends
 * requests (`harness.ready`, `conversation.ensure`, `turn.submit`, …) and we
 * send both notifications (`event`) and our own requests (`host.call`, for a
 * tool or an approval). A `host.call` is answered by the id-matched JSON-RPC
 * response, so there is no separate `host.result` method.
 */
import { log, replacer } from "./log.js";

/** A JSON-RPC error carrying the peer's numeric code. */
export class RpcError extends Error {
	constructor(code, message, data) {
		super(message ?? "rpc error");
		this.name = "RpcError";
		this.code = code;
		this.data = data;
	}
}

/** Guard against a malformed peer streaming an unbounded line. */
const MAX_LINE_BYTES = 64 * 1024 * 1024;

/**
 * @param {import("node:net").Socket} socket
 * @param {{
 *   handle?: (method: string, params: any) => Promise<any> | any,
 *   onNotify?: (method: string, params: any) => Promise<void> | void,
 *   onClose?: (error: Error | null) => void,
 * }} [handlers]
 */
export function createPeer(socket, { handle, onNotify, onClose } = {}) {
	let buffer = "";
	let seq = 0;
	/** @type {Map<string, {resolve: (v: any) => void, reject: (e: Error) => void}>} */
	const pending = new Map();
	let closed = false;
	let stopping = false;
	let closeError = null;
	let closeResolve;
	const closedPromise = new Promise((resolve) => {
		closeResolve = resolve;
	});

	socket.setNoDelay(true);
	socket.setEncoding("utf8");

	// Writes are serialized and respect backpressure so a large tool result or
	// event batch cannot interleave with, or overtake, a later frame.
	let writeChain = Promise.resolve();
	function send(message) {
		const line = `${JSON.stringify(message, replacer)}\n`;
		writeChain = writeChain.then(
			() =>
				new Promise((resolve) => {
					if (socket.destroyed) {
						resolve(undefined);
						return;
					}
					if (socket.write(line)) {
						resolve(undefined);
						return;
					}
					socket.once("drain", () => resolve(undefined));
				}),
		);
		return writeChain;
	}

	function finish(error) {
		if (closed) return;
		closed = true;
		closeError = error;
		for (const slot of pending.values()) slot.reject(error ?? new Error("bridge: closed"));
		pending.clear();
		closeResolve(error);
		if (onClose) onClose(error);
	}

	function handleLine(line) {
		let message;
		try {
			message = JSON.parse(line);
		} catch (error) {
			log.warn("bridge: dropping unparseable line", { bytes: line.length, error: String(error) });
			return;
		}
		if (message && typeof message.method === "string") {
			const params = message.params ?? {};
			if (message.id === undefined || message.id === null) {
				Promise.resolve()
					.then(() => onNotify?.(message.method, params))
					.catch((error) => log.error("bridge: notification handler failed", { method: message.method, error: error?.message ?? String(error) }));
				return;
			}
			const id = message.id;
			Promise.resolve()
				.then(() => handle?.(message.method, params))
				.then(
					(result) => send({ jsonrpc: "2.0", id, result: result === undefined ? null : result }),
					(error) =>
						send({
							jsonrpc: "2.0",
							id,
							error: {
								code: typeof error?.code === "number" ? error.code : -32000,
								message: error?.message ?? String(error),
								...(error?.data === undefined ? {} : { data: error.data }),
							},
						}),
				)
				.catch((error) => log.error("bridge: failed to answer request", { method: message.method, error: String(error) }));
			return;
		}
		const slot = pending.get(String(message?.id));
		if (!slot) {
			log.warn("bridge: response for unknown request id", { id: message?.id });
			return;
		}
		pending.delete(String(message.id));
		if (message.error) slot.reject(new RpcError(message.error.code, message.error.message, message.error.data));
		else slot.resolve(message.result);
	}

	function drain() {
		let index;
		while ((index = buffer.indexOf("\n")) >= 0) {
			const line = buffer.slice(0, index);
			buffer = buffer.slice(index + 1);
			if (line.trim()) handleLine(line);
		}
		if (buffer.length > MAX_LINE_BYTES) {
			finish(new Error(`bridge: frame exceeded ${MAX_LINE_BYTES} bytes`));
			socket.destroy();
		}
	}

	socket.on("data", (chunk) => {
		buffer += chunk;
		drain();
	});
	socket.on("error", (error) => finish(error));
	socket.on("close", () => {
		if (stopping) finish(null);
		else finish(new Error("bridge: host closed the connection"));
	});

	return {
		/** Send a request and await the id-matched response. */
		call(method, params, { timeoutMs = 0 } = {}) {
			if (closed) return Promise.reject(closeError ?? new Error("bridge: closed"));
			const id = `s${++seq}`;
			return new Promise((resolve, reject) => {
				let timer;
				const settle = (fn, value) => {
					if (timer) clearTimeout(timer);
					fn(value);
				};
				if (timeoutMs > 0) {
					timer = setTimeout(() => {
						pending.delete(id);
						reject(new Error(`bridge: ${method} timed out after ${timeoutMs}ms`));
					}, timeoutMs);
				}
				pending.set(id, {
					resolve: (value) => settle(resolve, value),
					reject: (error) => settle(reject, error),
				});
				send({ jsonrpc: "2.0", id, method, params }).catch(() => undefined);
			});
		},
		/** Send a one-way notification (no reply expected). */
		notify(method, params) {
			if (closed) return Promise.reject(closeError ?? new Error("bridge: closed"));
			return send({ jsonrpc: "2.0", method, params });
		},
		get closed() {
			return closed;
		},
		get closedPromise() {
			return closedPromise;
		},
		get error() {
			return closeError;
		},
		async close() {
			if (closed) return;
			stopping = true;
			await writeChain;
			await new Promise((resolve) => socket.end(resolve));
			finish(null);
		},
	};
}
