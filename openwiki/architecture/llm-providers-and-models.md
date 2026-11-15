---
type: "Reference"
title: "LLM providers, models and plan usage"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-552b6d83e1d1b259e32d81a4
    resource: repo://internal/llm/llm.go
  - id: openwiki-source-74033ec3d28fcbf92884e926
    resource: repo://internal/llm/providers.go
  - id: openwiki-source-40285323e46fc8aa4435a81a
    resource: repo://internal/llm/providers.json
  - id: openwiki-source-1bcc2fd189038a926ad3c31e
    resource: repo://internal/llm/usage.go
  - id: openwiki-source-2fcc0ac15ad0af123578c81f
    resource: repo://internal/server/handlers_chat.go
  - id: openwiki-source-de1a0014c6e11f4b35d4f8f2
    resource: repo://internal/server/handlers_providers.go
  - id: openwiki-source-8371a9932a71087cc7b05a85
    resource: repo://internal/server/handlers_usage.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# LLM providers, models and plan usage

Two different things live under "providers":

- **Where a model is** — the catalog: which endpoints exist, what models each one
  offers, and what each model can do (context window, max output, vision,
  reasoning levels). This is metadata, cached server-side, and a user can add a
  provider without touching the code.
- **How a request reaches it** — `llm.Client`: one OpenAI-compatible client that
  also carries the per-endpoint headers some gateways require.

A third, newer concern rides on both: **plan usage**. A subscription endpoint can
report how much of its allowance a key has spent, and v1 reads that through the
same client, with one small driver entry per endpoint family.

Turn mechanics are [Chat turns, queue, steer and the live stream](./chat-turns-and-queue.md);
the settings row these records are stored in is
[Store, sessions and settings](./store-sessions-and-settings.md); the composer
buttons that render a reading are in
[The SPA architecture](../frontend/spa-architecture.md).

## The catalog: snapshot, cache, additions

The catalog is a models.dev-shaped document (`Catalog` —
`internal/llm/providers.go:65-77`): `source`, `providers`, and `fetchedAt`.
Every `Provider` is `{id, name, baseURL, keyHint, doc, models, added}`
(`internal/llm/providers.go:54-63`) and every `ProviderModel` is
`{id, name, imageInput, reasoning, context, output}`
(`internal/llm/providers.go:40-52`), where `context`/`output` come from
models.dev's `limit.context` / `limit.output`.

Three layers, in fallback order:

```
GET /api/providers
  ├─ keyProvidersCache (settings)          ← rebuilt from models.dev when stale
  ├─ embedded providers.json               ← compiled into the binary
  ├─ + OpenRouter live directory           ← CatalogWithRouter
  ├─ + runtime-added providers              ← WithAdded(customProviders())
  ├─ + the trailing "custom" entry          ← WithCustom/customProvider
  └─ finalizeProviders: live /models lists per user
```

`providerCatalog()` (`internal/server/handlers_providers.go:257`) reads the
settings cache and falls back to `llm.EmbeddedCatalog()`
(`internal/llm/providers.go:98`, backed by `go:embed providers.json` at
`internal/llm/providers.go:19`). The shipped snapshot is
`{"source":"models.dev","providers":[…18 entries…]}`; it contains the two
OpenCode endpoints side by side — `opencode` ("OpenCode Zen",
`https://opencode.ai/zen/v1`, 40 models) and `opencode-go` ("OpenCode Go",
`https://opencode.ai/zen/go/v1`, 24 models), both with key hint
`OPENCODE_API_KEY`.

A cache is rebuilt when it is absent, when it predates one of the metadata fields,
or when it ages out — `catalogNeedsRefresh`
(`internal/server/handlers_providers.go:47`) with
`providerCatalogTTL = 24 * time.Hour`
(`internal/server/handlers_providers.go:43`). The `FetchedAt` timestamp exists
for exactly this reason: the catalog is served from the settings cache, so
without it a catalog built once would be served forever and any model models.dev
gained afterwards would keep an empty name, no context size and no reasoning
flags — a field check alone only notices a catalog built before a *field*
existed, never a model added *later*
(`internal/llm/providers.go:65-77`). `handleListProviders`
(`internal/server/handlers_providers.go:62`) refreshes from models.dev, rewrites
`keyProvidersCache`, merges OpenRouter's directory
(`CatalogWithRouter`, `internal/server/handlers_providers.go:249`) and appends
runtime-added providers.

The trailing `custom` provider ("Custom endpoint") is not part of the snapshot
and is never stored in the cache
(`internal/llm/providers.go:79-87`); `WithAdded` inserts runtime-added providers
*before* it and marks each `Added: true`
(`internal/llm/providers.go:109`).

### Per-user finalisation

`finalizeProviders` (`internal/server/handlers_providers.go:138`) is where the
catalog becomes this reader's picker:

- A provider the user saved is present even if the catalog has no entry for it
  (`Added: true`, base URL compared with `normalizeBase`).
- **Live model lists win over the static ones** for every provider that is
  saved, runtime-added, or public (NVIDIA, whose `/v1/models` needs no key —
  `NVIDIABaseURL`, `internal/llm/providers.go:133`). `customProviderModels`
  fetches them through a short-lived per-base-URL cache
  (`customModelsCache`, `internal/server/handlers_providers.go:85`) that is
  dropped by `invalidateCustomModelsCache` whenever provider configuration
  changes.
- The merge keeps the endpoint's list but re-applies static metadata for
  matching ids (name, `imageInput`, `reasoning`), so a model the picker offers
  keeps its rich flags.
- A model with no name from either side gets one derived by
  `llm.PrettifyModelName` (`internal/llm/providers.go:689`) instead of showing a
  bare id.
- **A provider's `models` is never `null`**: an unreachable endpoint would
  otherwise serialize a nil slice, and every client iterates it.

`maxModelsPerProvider = 1000` (`internal/llm/providers.go:128`) is a guard
against a pathological upstream, not a curation knob — a lower cap truncated
lists alphabetically, and the models past the cut lost their vision/reasoning/
context metadata, so a model the picker still offered came back as "text-only"
and `screenshot_app` was never offered.

## Adding a provider without a code change

Model metadata resolution starts from the base URL:
`ModelsForBaseURL` (`internal/llm/providers.go:139`) looks the URL up in
`knownBaseURLs` (`internal/llm/providers.go:181-197` — a pin table for providers
whose `api` field models.dev does not publish) and then in the catalog, comparing
`canonicalBaseURL` (`internal/llm/providers.go:172`, lower-cased, trailing slash
trimmed).

So adding a provider is a data operation:

1. **Save it as a provider record.** `PUT /api/settings` with
   `llm.providers[] = [{id, name, baseURL, model, apiKey}]` (records live in the
   `llm_providers` user setting) — this is the path for any endpoint, including
   ones models.dev has never heard of. The merge rules in `handlePutSettings`
   (`internal/server/handlers_auth.go:261`) keep the effective single-provider
   config in step with the saved record whose base URL matches, so a second
   provider can never silently overwrite the first.
2. **Or adopt one from models.dev.** `POST /api/providers/add` with `{"id"}`
   resolves it with `llm.FindProvider` and stores it as a custom provider
   (`handleAddProvider`, `internal/server/handlers_providers.go:330`); an id that
   is already in the catalog answers `{"ok":true,"existing":true}`.
   `POST /api/providers/remove` drops a runtime-added one.
3. **Nothing else.** `finalizeProviders` gives the new record an entry in
   `GET /api/providers`, fetches its live `/models` list, and the picker offers
   its models. No route, handler, or client change is needed — the model picker
   only needs the base URL and the key.

The two OpenCode endpoints need no catalog entry beyond what ships: both are in
`providers.json`, and even a hand-entered `https://opencode.ai/zen/go/v1` is
recognised because the request headers (below) are matched by host.

## One client, endpoint headers by host

`llm.Client` (`internal/llm/llm.go:58-73`) is the only thing that talks to a
model endpoint: `BaseURL`, `APIKey`, `Model`, `ReasoningEffort` (sent as
`reasoning_effort`), `SessionHeader`/`SessionID`, `StaticHeaders`, and an
`*http.Client`. `NewClient` (`internal/llm/llm.go:79`) derives the header set
from the base URL "so no caller has to know about it", and sets a 15-minute
timeout — generous enough that a long reasoning stream is never cut off, with the
chat handler's own context deadline as the real backstop.

`endpointHeadersByHost` (`internal/llm/providers.go:223-228`) pins what
`opencode.ai` needs:

```go
"opencode.ai": {
    SessionHeader: "x-opencode-session",
    Static:        map[string]string{"x-opencode-client": "v1"},
}
```

Two design points, both load-bearing:

- **Keyed on hostname, not on a path.** One API is served under several paths —
  `opencode.ai/zen/v1` and `opencode.ai/zen/go/v1` — so a path-keyed table
  silently stops matching the moment a deployment points at the other one, and
  the failure is a 400 that names no header.
- **v1 sends its own client name, not pi's.** The header attributes traffic;
  claiming to be `pi` would misattribute it. pi's remaining attribution headers
  (billing origin, `HTTP-Referer`, OpenRouter title, `User-Agent`) are telemetry,
  not protocol, and are deliberately not mirrored.

`applyEndpointHeaders` (`internal/llm/llm.go:97`) applies the static headers
first and then the session header, falling back to `c.Model` as the session value
because any non-empty value satisfies the endpoint; a caller-set header of the
same name always wins. Anything that calls an opencode endpoint — chat, model
lists, thinking probes, usage — must therefore go through `NewClient`, never a
bare `http.Get`.

## Model metadata beyond the snapshot

- **Live catalog** — `RefreshCatalog` (`internal/llm/providers.go:844`) and
  `SearchProviders` (`:920`) share a 15-minute in-memory cache
  (`liveCacheTTL`, `internal/llm/providers.go:254`), so browsing providers hits
  models.dev at most once per window.
- **Thinking** — `ProviderThinking(ctx, baseURL, apiKey, model)`
  (`internal/llm/providers.go:337`) answers "what effort levels does this model
  publish", caching for 10 minutes (`thinkingCacheTTL`, `:328`) and falling back
  to family heuristics (`familyThinking`, `:603`).
- **Context window** — `ModelContextLength(ctx, baseURL, apiKey, model)`
  (`internal/llm/providers.go:372`) reads the provider's own `/models`
  document, cached per `baseURL|model`. `fetchModelContext` (`:396`) accepts
  `context_length`, `max_context_length` or `context_window` because
  OpenAI-compatible providers disagree on the field name.
- **Vision** — `imageInput` on the catalog entry, checked per model
  (`CatalogHasVision`, `internal/llm/providers.go:807`). It gates which tools
  are offered (an image-input-only tool such as `screenshot_app` is hidden for a
  text-only model) and it is why a truncated catalog is not a cosmetic problem.

These feed the composer's **context ring**: `GET /api/projects/{id}/context`
(`handleContextUsage`, `internal/server/handlers_chat.go:1011`) recomputes the
session's prompt size, preferring the provider-reported `context` recorded on the
newest assistant message and estimating only what was appended after it.

## Plan usage: `GET /api/providers/usage`

A subscription endpoint reports allowance the way per-token pricing does on a
metered one. v1 exposes that as one endpoint:
`GET /api/providers/usage?providerId=<id>&force=1`
(`handleProviderUsage`, `internal/server/handlers_usage.go:74`, registered at
`internal/server/server.go` next to the thinking route).

Resolution: `providerId` is optional; the base URL and key come from
`llmConfig` and are overridden by the saved provider record when the id matches
one, and the saved record's `Name` is preferred over the driver's family label
because it is what the reader picked in the picker.

The response is `providerUsageView`
(`internal/server/handlers_usage.go:17`):

```json
{"supported": true,
 "providerId": "opencode-go", "provider": "OpenCode Go",
 "windows": [{"key":"rolling","label":"5 hour","percent":0,"status":"ok","resetsAt":"…"},
             {"key":"weekly","label":"Weekly","percent":17,"status":"ok","resetsAt":"…"},
             {"key":"monthly","label":"Monthly","percent":25,"status":"ok","resetsAt":"…"}],
 "fetchedAt": "…"}
```

`windows` is ordered **widest-last**, i.e. the order the rings draw them in,
outermost first. `percent` is the spent share, clamped to 0..100
(`clampPercent`, `internal/llm/usage.go:155`).

### The `{supported:false}` contract

`{"supported":false}` is an **HTTP 200 body, not an error**, and it means
"this endpoint publishes no usage" — the normal state for most providers:

| Situation | Response | Cached |
|---|---|---|
| Endpoint has no usage document (404/405, e.g. the paid `zen/v1` base) | `200 {"supported":false}` | 10 min (`usageUnsupported`) |
| No driver matches the base URL at all | `200 {"supported":false}` | 10 min |
| Provider does publish usage but this read failed (bad key, 5xx) | `200 {"supported":true,"error":"…"}` | never |
| Success | `200 {"supported":true,"windows":[…],"fetchedAt":…}` | 60 s (`usageCacheTTL`) |
| No base URL resolvable | `400 "provider is required"` | — |

The distinction matters: a failed read keeps the button *live* so the client can
show why it is empty, while an unsupported endpoint disables it. The client
mirrors that rule — an unsupported provider renders a disabled button rather than
an error. Readings are cached per `userID|baseURL`
(`providerUsageCache`, `internal/server/handlers_usage.go:35`) so a composer that
re-renders during a stream does not hammer a rate-limited usage endpoint, and
`force=1` bypasses the cache.

### Drivers: the one extension point

`internal/llm/usage.go` holds a small registry — `usageDriver{ID, Name, Match,
Path, Parse}` (`internal/llm/usage.go:57-77`) — and today exactly one entry
(`usageDrivers`, `:80`):

```go
{ID: "opencode-zen", Name: "OpenCode Zen",
 Match: func(baseURL string) bool { return usageHost(baseURL) == "opencode.ai" },
 Path:  "/usage",
 Parse: parseOpenCodeUsage}
```

`FetchUsage` (`internal/llm/usage.go:168`) then does the rest generically:

```
usageDriverFor(c.BaseURL) ── nil ──▶ ErrUsageUnsupported
        │
        ├─ GET c.BaseURL + driver.Path
        │    Authorization: Bearer <key> · Accept: application/json · applyEndpointHeaders
        ├─ 404/405 ──▶ ErrUsageUnsupported   (endpoint exists, no usage document)
        ├─ other non-2xx ──▶ "HTTP %d from %s%s"
        └─ 2xx ──▶ driver.Parse(body) ──▶ []UsageWindow
                    └─ ErrUsageUnsupported ──▶ ErrUsageUnsupported
                    └─ any other error ──▶ "unreadable usage from %s%s: %w"
```

Matching is on **host**, not on the provider id, for the same reason the header
table is: one API under several paths (`/zen/v1`, `/zen/go/v1`), and the id is
user-editable. That is why OpenCode Go works with no code of its own — it is
`opencode.ai`, so the driver matches, and `/zen/go/v1/usage` answers 200.

`parseOpenCodeUsage` (`internal/llm/usage.go:122`) reads the document

```json
{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"…"}, …}}
```

skips buckets that are absent, and names them itself: `rolling` is the
five-hour window (the endpoint names the span by how it rolls, not by its
length), so the display labels are `5 hour`, `Weekly`, `Monthly`. A body with no
known bucket at all returns `ErrUsageUnsupported`, while a body that is not
JSON is a plain error — a gateway returning an HTML page must not silently
disable a working provider.

**When a driver entry is needed, and when it is not:**

- *Not needed* — a new **base URL on a host the registry already matches**, or a
  provider that speaks the same shape. Save the provider record and the button
  fills in.
- *Not needed* — a provider that publishes nothing. It simply answers
  `{supported:false}` and the UI disables the button.
- *Needed* — a **new family**: a different host (`api.anthropic.com`,
  `chatgpt.com`), a different path, or a different document. That is one entry —
  a host predicate, a path, and a small parser. No route, client or UI change,
  which is exactly how Claude Code or Codex support would arrive
  (`internal/llm/usage.go:57-77`).

The response shape is deliberately provider-agnostic (`UsageWindow` carries
`key`, `label`, `percent`, `status`, `resetsAt`), so a driver that meters two
windows or five draws correctly without touching the client.

## Provider endpoints

| Route | Handler | Purpose |
|---|---|---|
| `GET /api/providers` | `handleListProviders` `internal/server/handlers_providers.go:62` | the finalised catalog for this user |
| `POST /api/providers/refresh` | `handleRefreshProviders` `:272` | re-fetch models.dev, rebuild the cache |
| `GET /api/providers/search` | `handleSearchProviders` `:319` | browse all models.dev providers |
| `POST /api/providers/add` / `remove` | `:330` / `:362` | runtime-added providers |
| `GET /api/providers/thinking` | `handleProviderThinking` `:292` | effort levels for one model |
| `GET /api/providers/usage` | `handleProviderUsage` `internal/server/handlers_usage.go:74` | plan allowance, `{supported:false}` when absent |
| `GET /api/projects/{id}/context` | `handleContextUsage` `internal/server/handlers_chat.go:1011` | prompt size for the context ring |

## Invariants

- Provider and model metadata is **data, not code**: adding an endpoint is a
  saved record, and the picker, its model list and its flags follow.
- Endpoint quirks are declared once, **by host**, and applied by `llm.Client`;
  nothing else constructs a request to a model or usage endpoint by hand.
- `models` is never `null` in a served catalog, and every model has a name.
- `{"supported":false}` is a 200 and means "nothing to show", never "error"; a
  failed usage read stays `supported:true` with a message and is not cached.
- A new usage family is one `usageDriver` entry — no route, client or UI change.
