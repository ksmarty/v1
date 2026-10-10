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

/**
 * Display metadata an extension declares for its tools.
 *
 * Kept separate from `toolNames` (a plain string list used for collision checks
 * and reporting) so nothing consuming that has to change. An extension opts in by
 * putting a `display` object on the tool:
 *
 *     defineTool({ name: "get_weather", display: { title: "Get weather",
 *       icon: "globe", summary: "{city}" }, ... })
 *
 * Only recognised keys survive, so a typo costs a nicety rather than putting
 * malformed metadata in front of v1.
 */
export function toolDisplay(extension) {
	const tools = Array.isArray(extension?.tools) ? extension.tools : [];
	const out = {};
	for (const tool of tools) {
		if (typeof tool?.name !== "string") continue;
		const source = tool.display;
		if (!source || typeof source !== "object") continue;
		const entry = {};
		if (typeof source.title === "string") entry.title = source.title;
		if (typeof source.icon === "string") entry.icon = source.icon;
		if (typeof source.summary === "string") entry.summary = source.summary;
		if (Object.keys(entry).length > 0) out[tool.name] = entry;
	}
	return out;
}

/**
 * Settings fields an extension declares, for the form v1 renders in its
 * settings popup.
 *
 * An extension opts in by listing fields:
 *
 *     settings: [
 *       { key: "limit", label: "Max words", type: "text", default: "500" },
 *       { key: "strict", label: "Strict mode", type: "checkbox" },
 *     ]
 *
 * Two field types, text and checkbox, cover what an extension needs to be
 * configurable without growing a form builder. Only recognised keys survive, so
 * a typo costs a field rather than putting malformed input in front of v1.
 *
 * The values themselves live in v1's store, not in the extension, so they
 * survive a reload and can be edited while the extension is disabled.
 */
export function settingsSchema(extension) {
	const fields = Array.isArray(extension?.settings) ? extension.settings : [];
	const out = [];
	for (const field of fields) {
		if (!field || typeof field !== "object") continue;
		const key = field.key;
		// The key indexes an object in v1's store and is shown to the user, so
		// reject anything that could not round-trip through JSON as a name.
		if (typeof key !== "string" || !/^[A-Za-z][A-Za-z0-9_-]*$/.test(key)) continue;
		const type = field.type === "checkbox" ? "checkbox" : "text";
		const entry = { key, type };
		if (typeof field.label === "string" && field.label.trim()) entry.label = field.label.trim();
		if (typeof field.help === "string" && field.help.trim()) entry.help = field.help.trim();
		if (type === "checkbox") {
			entry.default = field.default === true;
		} else if (typeof field.default === "string") {
			entry.default = field.default;
		}
		out.push(entry);
	}
	return out;
}

/** The keys of the prompt sections an extension contributes. */
export function sectionKeys(extension) {
	const sections = Array.isArray(extension?.sections) ? extension.sections : [];
	return sections
		.map((entry) => entry?.key ?? entry?.name ?? entry?.id)
		.filter((key) => typeof key === "string");
}

/**
 * The hooks an extension registers, as "task:handler+handler" labels.
 *
 * `hook(task, handlers)` stores the task name and the handler keys, so this
 * reads the registration rather than the running hook. It is what lets the
 * create_extension result confirm that hooks loaded — a tool and a section
 * show up in the tool/section lists, but a hook has no other visible surface.
 */
export function hookNames(extension) {
	const hooks = Array.isArray(extension?.hooks) ? extension.hooks : [];
	const out = [];
	for (const entry of hooks) {
		const task = entry?.task ?? entry?.name;
		if (typeof task !== "string" || task === "") continue;
		const handlers =
			entry?.handlers && typeof entry.handlers === "object" ? Object.keys(entry.handlers) : [];
		out.push(handlers.length ? `${task}:${handlers.join("+")}` : task);
	}
	return out;
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
export async function loadExtensions(root, api, enabledIds, settingsFor) {
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
			// The values the user set in v1 are handed to the factory, because a
			// module outside node_modules has no other way to reach them. Only the
			// factory form gets them: the object form is constructed before the
			// loader can know which extension it is.
			const scoped = settingsFor ? { ...api, settings: settingsFor(id) } : api;
			const extension = typeof exported === "function" ? await exported(scoped) : exported;
			if (!extension || typeof extension !== "object") {
				throw new Error("the extension must be an object, or a function returning one");
			}
			// The registry name is forced to the directory id, so what the agent
			// sees cannot drift from what is on disk.
			extensions.push({
				id,
				name: id,
				extension: { ...extension, name: id },
				settings: settingsSchema(extension),
			});
		} catch (error) {
			const message = error?.message ?? String(error);
			errors.push({ id, message });
			log.warn("extensions: load failed", { id, error: message });
		}
	}
	return { extensions, errors };
}
