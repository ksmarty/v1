/**
 * Structured logging for the sidecar.
 *
 * Everything goes to stderr: stdout stays free for the Go supervisor to treat
 * as a log stream, and no protocol traffic ever uses a stdio channel (the
 * bridge is a Unix socket).
 */
const LEVELS = { debug: 10, info: 20, warn: 30, error: 40 };

const threshold = LEVELS[process.env.V1_SIDECAR_LOG ?? "info"] ?? LEVELS.info;
const started = Date.now();

function render(fields) {
	if (!fields) return "";
	const parts = [];
	for (const [key, value] of Object.entries(fields)) {
		if (value === undefined) continue;
		const text = typeof value === "string" ? value : JSON.stringify(value, replacer);
		parts.push(`${key}=${text}`);
	}
	return parts.length ? ` ${parts.join(" ")}` : "";
}

/** BigInt is the only non-JSON value pi-durable hands us; render it as a string. */
function replacer(_key, value) {
	return typeof value === "bigint" ? value.toString() : value;
}

function emit(level, message, fields) {
	if (LEVELS[level] < threshold) return;
	const ms = String(Date.now() - started).padStart(7);
	process.stderr.write(`[v1-sidecar ${ms}ms] ${level} ${message}${render(fields)}\n`);
}

export const log = {
	debug: (message, fields) => emit("debug", message, fields),
	info: (message, fields) => emit("info", message, fields),
	warn: (message, fields) => emit("warn", message, fields),
	error: (message, fields) => emit("error", message, fields),
};

export { replacer };
