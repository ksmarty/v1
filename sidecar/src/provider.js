/**
 * Per-conversation provider registration (plan F5).
 *
 * Go owns v1's model catalog and the user's credentials; it sends a fully
 * resolved provider + model descriptor with every `conversation.ensure`. We
 * register that as a pi-ai provider at runtime and select it per conversation.
 *
 * The provider id is suffixed with a hash of (baseUrl, apiKey) so two users
 * with different keys — or one user with two keys for the same upstream — get
 * distinct registry entries instead of overwriting each other.
 */
import { createHash } from "node:crypto";

import { createModels, createProvider } from "@earendil-works/pi-ai";
import { stream as openaiStream, streamSimple as openaiStreamSimple } from "@earendil-works/pi-ai/api/openai-completions";

import { log } from "./log.js";

/** Models registered so far: key → signature of the descriptor it was built from. */
const registered = new Map();

/** A stable, collision-resistant provider id for this upstream + credential. */
export function providerKey(config) {
	const digest = createHash("sha256")
		.update(`${config.baseUrl ?? ""}\u0000${config.apiKey ?? ""}`)
		.digest("hex")
		.slice(0, 12);
	return `${config.id}#${digest}`;
}

function signatureOf(config) {
	return JSON.stringify({
		baseUrl: config.baseUrl,
		api: config.api,
		sessionHeader: config.sessionHeader,
		models: (config.models ?? []).map((model) => model.id),
	});
}

/**
 * Fill in every field pi-ai requires on a chat model. v1's catalog is thinner
 * than pi-ai's `Model` (it has no pricing, for one), and pi-ai dereferences
 * several of these unconditionally — `model.cost.tiers` in the usage
 * accounting, `model.input.includes(...)` in the message transforms,
 * `model.maxTokens` when building a request — so a missing field is a crash,
 * not a default. Cost stays zero: v1's catalog publishes no prices, so the
 * turn reports tokens but no spend.
 */
function modelDescriptor(model, key, config) {
	const contextWindow = Number.isFinite(model.contextWindow) && model.contextWindow > 0 ? model.contextWindow : 0;
	return {
		...model,
		id: model.id,
		name: model.name ?? model.id,
		api: model.api ?? config.api ?? "openai-completions",
		// The registry looks models up by this key; createProvider does not stamp it.
		provider: key,
		// detectCompat dereferences baseUrl unconditionally.
		baseUrl: model.baseUrl ?? config.baseUrl,
		// `input.includes("image")` is called on every request, so text-only
		// models must still carry an explicit list.
		input: Array.isArray(model.input) && model.input.length > 0 ? model.input : ["text"],
		reasoning: model.reasoning === true,
		cost: {
			input: model.cost?.input ?? 0,
			output: model.cost?.output ?? 0,
			cacheRead: model.cost?.cacheRead ?? 0,
			cacheWrite: model.cost?.cacheWrite ?? 0,
			...(model.cost?.tiers ? { tiers: model.cost.tiers } : {}),
		},
		contextWindow,
		maxTokens:
			Number.isFinite(model.maxTokens) && model.maxTokens > 0
				? model.maxTokens
				: contextWindow > 0
					? Math.min(8192, contextWindow)
					: 8192,
	};
}

/**
 * Register (or refresh) the provider and return the model reference to store on
 * the conversation.
 *
 * @returns {{ providerId: string, modelId: string }}
 */
export function registerProvider(models, config) {
	if (!config || !config.id) throw new Error("provider config requires an id");
	if (!config.baseUrl) throw new Error(`provider ${config.id}: baseUrl is required`);

	const key = providerKey(config);
	const signature = signatureOf(config);
	if (registered.get(key) === signature) {
		const modelId = pickModelId(config);
		return { providerId: key, modelId };
	}

	const descriptors = (config.models ?? []).map((model) => modelDescriptor(model, key, config));
	if (descriptors.length === 0) throw new Error(`provider ${config.id}: no models supplied`);

	const sessionHeader = config.sessionHeader;
	/** Inject the upstream's per-session routing header, when it needs one. */
	const wrap = (fn) => (model, context, options) =>
		fn(
			model,
			context,
			sessionHeader
				? {
						...options,
						headers: {
							...(options?.headers ?? {}),
							[sessionHeader]: options?.sessionId ?? config.id,
						},
					}
				: options,
		);

	const apiName = config.api ?? "openai-completions";
	if (apiName !== "openai-completions") {
		throw new Error(`provider ${config.id}: unsupported api ${apiName}`);
	}
	const api = { stream: wrap(openaiStream), streamSimple: wrap(openaiStreamSimple) };

	models.setProvider(
		createProvider({
			id: key,
			name: config.name ?? config.id,
			baseUrl: config.baseUrl,
			models: descriptors,
			auth: config.apiKey
				? {
						apiKey: {
							name: `${config.id} API key`,
							resolve: async () => ({ auth: { apiKey: config.apiKey }, source: "v1" }),
						},
					}
				: undefined,
			...(api ? { api } : {}),
		}),
	);
	registered.set(key, signature);
	log.info("provider registered", {
		provider: config.id,
		key,
		models: descriptors.map((model) => model.id).join(","),
		sessionHeader: sessionHeader ?? "(none)",
	});
	return { providerId: key, modelId: pickModelId(config) };
}

function pickModelId(config) {
	const wanted = config.modelId;
	const descriptors = config.models ?? [];
	const found = wanted ? descriptors.find((model) => model.id === wanted) : descriptors[0];
	if (!found) {
		throw new Error(
			`provider ${config.id}: model ${wanted ?? "(unspecified)"} is not in the supplied catalog [${descriptors
				.map((model) => model.id)
				.join(", ")}]`,
		);
	}
	return found.id;
}

/** A fresh pi-ai model store, owned by the sidecar process. */
export function createModelStore() {
	return createModels();
}
