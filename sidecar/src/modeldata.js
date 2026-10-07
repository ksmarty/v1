/**
 * pi's builtin model catalog, read through pi's own API.
 *
 * v1's catalog (internal/llm/providers.json) is hand-maintained and does not
 * know every model a user can select — `deepseek-v4.1-flash` is one — and Go
 * sends no `contextWindow` at all for a model it does not know (`omitempty`
 * drops the field). pi-ai ships the same model data it uses for its own
 * compaction and max_tokens decisions, so read that rather than guess.
 *
 * Two consumers:
 *   - modelDescriptor() in provider.js fills in a context window the descriptor
 *     is missing, so pi-ai is not told the model has none;
 *   - Go reads the snapshot writeModelCatalog() leaves in the data dir, because
 *     it cannot reach pi's data itself (internal/server/pimodels.go).
 */
import { renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
	getBuiltinModelDataGeneratedAt,
	getBuiltinModels,
	getBuiltinProviders,
} from "@earendil-works/pi-ai/providers/all";

import { log } from "./log.js";

/** The file name Go looks for in the data dir. */
export const MODEL_CATALOG_FILE = "pi-models.json";

let index;

/**
 * Every builtin chat model, keyed by id. Built once — the catalog is static for
 * the life of the process. One id can appear under several providers; the entry
 * with the largest context window wins, so a provider that reports a narrower
 * window for the same model cannot shrink it.
 */
export function builtinModelIndex() {
	if (index) return index;
	index = new Map();
	for (const provider of getBuiltinProviders()) {
		let models;
		try {
			models = getBuiltinModels(provider);
		} catch (err) {
			log.debug("model catalog: skipping provider", { provider, error: String(err) });
			continue;
		}
		for (const model of models ?? []) {
			if (!model || typeof model.id !== "string" || model.id === "") continue;
			const prev = index.get(model.id);
			if (!prev || (model.contextWindow ?? 0) > (prev.contextWindow ?? 0)) {
				index.set(model.id, model);
			}
		}
	}
	log.info("model catalog: loaded", { models: index.size });
	return index;
}

/** pi's catalog entry for a model id, or null when pi does not know it. */
export function builtinModel(modelId) {
	if (!modelId) return null;
	return builtinModelIndex().get(modelId) ?? null;
}

/** The catalog in the shape Go consumes. */
export function modelCatalog() {
	const models = {};
	for (const [id, model] of builtinModelIndex()) {
		models[id] = {
			name: model.name ?? id,
			contextWindow: model.contextWindow ?? 0,
			maxTokens: model.maxTokens ?? 0,
			reasoning: model.reasoning === true,
		};
	}
	return {
		generatedAt: getBuiltinModelDataGeneratedAt?.() ?? 0,
		source: "@earendil-works/pi-ai",
		models,
	};
}

/**
 * Write the catalog where Go can read it. Written to a temp file and renamed,
 * so a reader never sees a half-written catalog.
 */
export function writeModelCatalog(dir) {
	if (!dir) return;
	const catalog = modelCatalog();
	const count = Object.keys(catalog.models).length;
	if (count === 0) {
		log.warn("model catalog: empty, not writing", {});
		return;
	}
	const tmp = join(dir, `${MODEL_CATALOG_FILE}.tmp`);
	const dest = join(dir, MODEL_CATALOG_FILE);
	try {
		writeFileSync(tmp, JSON.stringify(catalog));
		renameSync(tmp, dest);
		log.info("model catalog: wrote", { path: dest, models: count });
	} catch (err) {
		log.warn("model catalog: could not write", { path: dest, error: String(err) });
	}
}
