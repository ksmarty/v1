/**
 * User extensions.
 *
 * An extension is a JS module at `<root>/<id>/index.js`. Its default export is
 * either a factory called with the extension API, or a ready-made extension
 * object.
 *
 * The factory form is the documented one: the file sits outside any
 * node_modules, so it cannot `import` pi-durable itself — the API has to be
 * injected. The object form stays supported because it needs nothing but its
 * own code.
 *
 * A broken extension must never take the sidecar down. The sidecar serves every
 * conversation, so one bad file degrades to "not loaded, with an error
 * reported" and the rest keep working.
 */
import { readdir, stat } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { log } from "./log.js";

/** The id is a directory name, so this is a path-safety guard, not cosmetics. */
const ID_PATTERN = /^[a-z0-9][a-z0-9-]*$/;

/**
 * Bind a tool to the conversation it is offered in.
 *
 * pi-durable hands `execute` its arguments, the API and a Context, and none of
 * them name the conversation the call came from: a prompt section's render does
 * receive a `conversationId`, but a tool does not. The conversation is known
 * here, where the tool list is built for one conversation, so it is captured in
 * a closure and carried through async context.
 *
 * AsyncLocalStorage rather than a field on the host, because two conversations
 * can run turns at the same time: a shared field would let one call read the
 * other's conversation.
 *
 * The id is read from the binding at call time, because the tool list is
 * assembled before a newly created conversation has an id.
 */
export function bindToolToConversation(tool, binding, storage) {
	if (!binding) return tool;
	return {
		...tool,
		execute: (args, api, context) => {
			const conversationId = binding.conversationId;
			if (!conversationId) return tool.execute(args, api, context);
			return storage.run(conversationId, () => tool.execute(args, api, context));
		},
	};
}

/** The tool names an extension contributes, for collision checks and reporting. */
export function toolNames(extension) {
	const tools = Array.isArray(extension?.tools) ? extension.tools : [];
	return tools.map((tool) => tool?.name).filter((name) => typeof name === "string");
}

/** The keys of the prompt sections an extension contributes. */
export function sectionKeys(extension) {
	const sections = Array.isArray(extension?.sections) ? extension.sections : [];
	return sections
		.map((entry) => entry?.key ?? entry?.name ?? entry?.id)
		.filter((key) => typeof key === "string");
}

/**
 * Load every enabled extension under `root`, calling each factory with `api`.
 *
 * `enabledIds` is the set of ids the user has enabled, or null to load
 * everything. A disabled extension is not imported at all: its factory is code
 * the user asked not to run, so skipping the import matters, not just skipping
 * the install.
 *
 * Never throws: a missing directory is simply "no extensions", and a module
 * that fails to load is reported in `errors` so the caller can surface it
 * without losing the extensions that did load.
 */
export async function loadExtensions(root, api, enabledIds) {
	const extensions = [];
	const errors = [];
	if (!root) return { extensions, errors };

	let entries;
	try {
		entries = await readdir(root, { withFileTypes: true });
	} catch (error) {
		if (error?.code !== "ENOENT") {
			errors.push({ id: "", message: `cannot read ${root}: ${error?.message ?? String(error)}` });
			log.warn("extensions: cannot read the extensions directory", {
				root,
				error: error?.message ?? String(error),
			});
		}
		return { extensions, errors };
	}

	for (const entry of entries) {
		if (!entry.isDirectory()) continue;
		const id = entry.name;
		if (!ID_PATTERN.test(id)) {
			errors.push({ id, message: "invalid extension id (use lowercase letters, digits and dashes)" });
			continue;
		}
		if (enabledIds && !enabledIds.has(id)) continue;
		const source = join(root, id, "index.js");
		try {
			// Cache-bust on mtime so an edited file is picked up by a reload:
			// Node caches ES modules by URL, so an import of the same path would
			// otherwise keep serving the first version for the process's life.
			const info = await stat(source);
			const module = await import(`${pathToFileURL(source).href}?v=${info.mtimeMs}`);
			const exported = module.default ?? module.extension;
			if (exported === undefined) throw new Error("the module has no default export");
			const extension = typeof exported === "function" ? await exported(api) : exported;
			if (!extension || typeof extension !== "object") {
				throw new Error("the extension must be an object, or a function returning one");
			}
			// The registry name is forced to the directory id, so what the agent
			// sees cannot drift from what is on disk.
			extensions.push({ id, name: id, extension: { ...extension, name: id } });
		} catch (error) {
			const message = error?.message ?? String(error);
			errors.push({ id, message });
			log.warn("extensions: load failed", { id, error: message });
		}
	}
	return { extensions, errors };
}
