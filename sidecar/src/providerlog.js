/**
 * Provider request logging (observational only).
 *
 * When a model call is rejected, the exchange itself is the evidence: which
 * endpoint and model were asked for, and what came back. pi-ai surfaces the
 * provider's message — that is how "The string did not match the expected
 * pattern" reaches the user — but not the status, the request that carried it,
 * or the rest of the body. In the pi-durable harness path Go never sees the
 * answer at all, because the call is made here.
 *
 * So the sidecar wraps global fetch: a failed request is logged with its status
 * and the start of the endpoint's body. The response is returned untouched, and
 * nothing here may throw into the caller — this exists to explain a failure, not
 * to cause one.
 */
import { log } from "./log.js";

/** How much of a failed response body is kept. Enough for an API error payload. */
const MAX_BODY = 2000;

/** How much of a request body is scanned for the model/stream fields. */
const MAX_HEAD = 2000;

let installed = false;

function urlOf(input) {
	try {
		if (typeof input === "string") return input;
		if (input instanceof URL) return input.href;
		if (input && typeof input.url === "string") return input.url;
	} catch {
		// Fall through to the placeholder.
	}
	return "(unreadable)";
}

/** Request facts worth having next to a rejection. Never header values: they carry the API key. */
function requestInfo(input, init) {
	const info = { url: urlOf(input), method: String(init?.method ?? input?.method ?? "GET").toUpperCase() };
	try {
		const headers = new Headers(init?.headers ?? input?.headers);
		info.headers = [...headers.keys()].sort().join(",");
	} catch {
		// Headers we cannot enumerate are not worth failing over.
	}
	try {
		const body = init?.body;
		if (typeof body === "string") {
			info.bytes = body.length;
			// Only the head is scanned: a turn's body is the whole transcript, and
			// parsing it here would cost more than the log is worth. The model and
			// stream fields sit at the front of an OpenAI-compatible request.
			const head = body.slice(0, MAX_HEAD);
			const model = /"model"\s*:\s*"([^"]{0,200})"/.exec(head);
			if (model) info.model = model[1];
			const stream = /"stream"\s*:\s*(true|false)/.exec(head);
			if (stream) info.stream = stream[1] === "true";
		} else if (body && typeof body.byteLength === "number") {
			info.bytes = body.byteLength;
		}
	} catch {
		// Same: a body we cannot read must not affect the request.
	}
	return info;
}

/**
 * Wraps global fetch so a rejected model call leaves evidence in the sidecar log
 * (which the Go supervisor forwards to the v1 server log). Idempotent.
 */
export function installProviderFetchLog() {
	if (installed) return;
	installed = true;
	const original = globalThis.fetch;
	if (typeof original !== "function") return;

	globalThis.fetch = async (input, init) => {
		const info = requestInfo(input, init);
		const started = Date.now();
		let res;
		try {
			res = await original.call(globalThis, input, init);
		} catch (error) {
			// A transport failure never reaches the endpoint, but it is the whole
			// story for the turn.
			log.error("provider request failed", {
				...info,
				ms: Date.now() - started,
				error: error?.message ?? String(error),
			});
			throw error;
		}
		if (!res || res.ok) {
			log.debug("provider request", { ...info, ms: Date.now() - started, status: res?.status });
			return res;
		}
		// Only a failed response is read, and only from a clone: a 2xx here is
		// usually a streaming body that must not be buffered.
		let body = "";
		try {
			body = (await res.clone().text()).slice(0, MAX_BODY);
		} catch {
			body = "(body unreadable)";
		}
		log.error("provider rejected the request", {
			...info,
			ms: Date.now() - started,
			status: res.status,
			body,
		});
		return res;
	};
}
