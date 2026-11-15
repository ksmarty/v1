---
type: "Reference"
title: "Memory, embeddings, web search and sanitisation"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
  - id: openwiki-source-dc8ff05d99ba4de7877a87e7
    resource: repo://internal/agent/tools.go
  - id: openwiki-source-1b7d025e07bd0518741e2df8
    resource: repo://internal/embed/bert.go
  - id: openwiki-source-c1c33d07645f18c3bd10a8b4
    resource: repo://internal/embed/embed.go
  - id: openwiki-source-9cee57d9e32844cf3f8ce40c
    resource: repo://internal/embed/native.go
  - id: openwiki-source-552b6d83e1d1b259e32d81a4
    resource: repo://internal/llm/llm.go
  - id: openwiki-source-6463cc27417ca7a3122843f8
    resource: repo://internal/memory/capture.go
  - id: openwiki-source-f2db9652888c2aca52322cc0
    resource: repo://internal/memory/private.go
  - id: openwiki-source-ff6f999fc23a880a3d1bd7d2
    resource: repo://internal/memory/rank.go
  - id: openwiki-source-3036029a9693cf86ac6af322
    resource: repo://internal/memory/tags.go
  - id: openwiki-source-8ffccfbabd0b7afa97102b23
    resource: repo://internal/sanitize/sanitize.go
  - id: openwiki-source-57cd9af307edaf4982ff6311
    resource: repo://internal/server/handlers_memories.go
  - id: openwiki-source-1ae79601e38b9e7ef80afd4d
    resource: repo://internal/server/memory_capture.go
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
  - id: openwiki-source-d6e7122d5c693057fc27bc68
    resource: repo://internal/websearch/websearch.go
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Memory, embeddings, web search and sanitisation

Four small, deliberately boring packages sit under the model's input path:

| Package | Job | Needs a provider? |
|---|---|---|
| `internal/memory` | score and extract the project's stored memories | no — pure functions |
| `internal/embed` | turn text into vectors (three providers, one of them in-process) | optional |
| `internal/websearch` | the `web_search` tool's backend | yes — per-user key |
| `internal/sanitize` | strip what a provider must never receive | no — always on |

The rule they share: **nothing here is required for the agent to work**. With no
embedding provider the ranker falls back to lexical matching, which is what v1 did
before embeddings existed (`internal/embed/embed.go:16-17`); with no web-search key
the tool is not offered at all, so it can never be called and fail
(`internal/websearch/websearch.go:1-6`); sanitisation has no off switch, because
it is the one thing standing between a project file and an outbound request.

Memory is written by the agent's `remember` tool
([Agent tools, tool results and events](./agent-tools-and-events.md)) and stored
per project ([Store, sessions and settings](./store-sessions-and-settings.md)).
This page is about what happens between the store and the prompt.

## Selection: what gets injected, and what is refused

`Rank` (`internal/memory/rank.go:273`) scores every enabled candidate and returns
the best ones. Everything about it is pure — candidates and their vectors come in,
scored hits come out — so it is testable without a database or a provider
(`internal/memory/rank.go:1-13`).

```
candidate ──┬── Cosine(query, content vector)   ┐
            ├── TagMatch(query, tags)           ├─▶ Relevance ─┐
            └── Keyword(query, content, tags)   ┘              ├─▶ Score = 0.80·rel
                                                               │      + 0.12·importance
                     importance (0..3 → 0..1) ─────────────────┤      + 0.08·recency
                     recency (30-day half-life) ───────────────┘
```

- **Relevance dominates.** The weights are 0.80 relevance, 0.12 importance, 0.08
  recency (`internal/memory/rank.go:55-58`): importance and recency can reorder
  memories that are equally on-topic, but cannot lift an off-topic one above a
  relevant one.
- **Inside relevance**, the content vector leads at 0.5, tags are a strong second
  at 0.3, and a lexical term contributes 0.2
  (`internal/memory/rank.go:64-67`). The lexical term is there to catch exact
  identifiers — file names, symbol names — that embeddings blur.
- **No provider, no vectors**: the blend collapses to
  `0.7·keyword + 0.3·tagSim` (`Relevance`, `internal/memory/rank.go:224-232`), so
  memory still works from a cold install.
- **A floor exists.** `MinRelevance = 0.25`
  (`internal/memory/rank.go:74`) is the score below which a memory is simply not
  about this turn; injecting it would cost context and distract the model. The
  value is lower than opencode-mem's 0.6 because this scale is a blend, not a raw
  cosine.
- **Pinned memories are exempt.** Importance ≥ `PinnedThreshold = 2`
  (`internal/memory/rank.go:264`) survives the floor and the limit — a memory
  recorded with "importance 2+ pins it so it never decays" is by definition
  relevant in every turn.
- **Recency** halves every `RecencyHalfLifeDays = 30`
  (`internal/memory/rank.go:86`) and falls back to age when a memory was never
  retrieved.
- **`Cosine` returns 0 for a length mismatch or an empty vector, not a wrong
  number** (`internal/memory/rank.go:92-108`) — that is how a model swap that
  would mix two vector spaces is caught rather than silently mis-scoring.

The system-prompt budget is applied after ranking: `memoryTopK = 8`
(`internal/server/handlers_memories.go:30`) memories, at most
`memoryBudgetChars = 4000` characters
(`internal/server/handlers_memories.go:25`), rendered as
`Project memories (the best matches for this message, not the whole store — search_memories looks at the rest; forget with an id deletes one):`
followed by `- [id] category: content` lines. Each injected hit is touched, so
recency reflects real use.

Two different floors exist on purpose: injection uses `memory.MinRelevance`
(0.25), while the `search_memories` tool uses `searchFloor = 0.05`
(`internal/agent/tools.go:511`) because an explicit search should return the
weak-matching memories the automatic path deliberately withholds.

The memories page and the chat path fetch vectors differently:
`MemoryVectors` loads them separately so the page, which renders every row, never
pays for kilobytes of floats it cannot use
(`internal/server/handlers_memories.go:124-129`). A query-embedding failure
degrades to lexical ranking rather than dropping the section — a provider outage
must not silently empty the model's memory.

## Extraction: what may become a memory

`<private>` stripping happens twice, and both are structural rather than
instructions:

- `StripPrivate` (`internal/memory/private.go:24`) removes every
  `<private>…</private>` span, and an unclosed `<private>` is stripped **to the
  end of the text** so a missing closing tag cannot leak the remainder. It runs
  on the way in — the `remember` tool calls it before storing
  (`internal/agent/tools.go:553`) — because a memory is re-injected into later
  prompts, so a leaked credential would be repeated for as long as the memory
  lives. A memory that is entirely private becomes empty, which callers refuse.
- `NormalizeTags` (`internal/memory/tags.go:7`) lowercases, trims, dedupes and
  caps a tag list at 12 entries so a runaway list cannot bloat the row.

Automatic capture is a model call, so it is **off unless
`memory_auto_capture` is set** (`keyMemoryAutoCapture`,
`internal/server/server.go` — the setting comment spells out the cost). When on,
`captureMemories` (`internal/server/memory_capture.go:32`) runs *detached from
the request*, after the `done` event, with `captureTimeout = 90s` and
`knownMemoryLimit = 60` existing memories listed to suppress repeats. It writes
through the same path as the `remember` tool — same private stripping, category
validation, deduplication, capacity limit and embedding — using a **fresh
executor**, because the turn's executor carries SSE callbacks and a background
task must not write to a closed stream.

What the extractor is allowed to see, and what it must produce:

- `TurnExchange` (`internal/memory/capture.go:133`) renders **only** the user's
  messages and the assistant's final reply. The transcript is excluded so the
  model cannot re-derive its own earlier memories (compounding paraphrase until
  retrieval degrades), and tool output is excluded as bulk with little durable
  content.
- `CaptureSystem` (`internal/memory/capture.go:16-40`) demands a JSON array of
  `{content, category ∈ preference|episodic|fact|plan, tags, importance 1-3}`,
  forbids progress reports, restatements of the request, and anything already
  known, and caps the reply at 5 entries.
- `ParseCaptured` (`internal/memory/capture.go:56`) returns nothing rather than a
  partial guess when the reply will not parse — the next turn extracts again
  anyway.
- Deduplication is at the door: a new memory whose cosine against an existing one
  is ≥ `DedupThreshold = 0.90` (`internal/memory/rank.go:83`) is refused, because
  v1's memories are injected into every prompt and the same fact accumulating
  twice wastes context forever.

## The embedding encoder is pure Go, and `CGO_ENABLED=0` is why

`embed.Config` (`internal/embed/embed.go:46-52`) has three providers, and the
choice is per user (`embedConfigFor`, `internal/server/server.go:655`, reading
`keyEmbedProvider/Model/BaseURL/APIKey`):

| Provider | Transport | Key required |
|---|---|---|
| `native` | in-process BERT encoder, weights fetched from Hugging Face on first use | no |
| `openai` | any OpenAI-compatible `/embeddings` | only by the endpoint's own rules |
| `huggingface` | HF inference feature-extraction (`DefaultHuggingFaceURL`, `internal/embed/embed.go:43`) | yes |

`native` is the only provider that needs no external service and the only one
that works offline once its model is cached — and it is **pure Go because the
binary is built with `CGO_ENABLED=0`** (`Dockerfile:31`, and `ci.yml:70` builds
the tree the same way), which rules out an ONNX runtime
(`internal/embed/embed.go:12-14`). The transformer encoder is a small, fixed
sequence of matrix multiplies, layer norms and a softmax, so `bert.go`
implements the forward pass directly (`internal/embed/bert.go:1-8`) instead of
linking a runtime.

That implementation is deliberately narrow, and refusal is explicit rather than
silent:

- `supportedModels` accepts only `"bert"` (`internal/embed/bert.go:50-52`) — the
  plain encoder family (all-MiniLM, bge-*, gte-*, e5-*, jina-v2-*,
  snowflake-arctic-embed). Gemma, MPNet, DeBERTa, ModernBERT and RoBERTa are
  refused at load rather than producing quietly wrong vectors.
- `PositionEmbeddingType` must be absolute; RoBERTa-style learned offsets and
  rotary variants would need a different embedding step
  (`internal/embed/bert.go:41-43`).
- Pooling is detected (`PoolMean` or `PoolCLS`, `internal/embed/bert.go:22-25`),
  because two model cards with identical weights disagree about it.
- `dims` (`internal/embed/embed.go:80-102`) pins the vector width of known model
  ids so a model swap cannot mix vector spaces; `DimsFor` reports 0 for a native
  model whose files are not downloaded yet, which the settings page shows as
  unknown rather than guessing a width that would then be wrong.
- `usesTaskPrefixes` (`internal/embed/embed.go:190`) adds the retrieval
  instruction prefixes Nomic models are trained with — they retrieve noticeably
  worse without them — so `EmbedQuery` and `EmbedDocument` differ on purpose
  (`internal/embed/embed.go:240-263`).
- The default native model is `sentence-transformers/all-MiniLM-L6-v2`
  (`internal/embed/native.go:30`). Files land in `V1_MODEL_CACHE`, else
  `$V1_DATA_DIR/models`, else the user cache dir, flattened one level per repo
  (`modelCacheDir`, `internal/embed/native.go:93-111`), fetched with a
  20-minute per-file timeout (`nativeFileTimeout`, `:35`) from
  `V1_HF_ENDPOINT` or `https://huggingface.co` (`hfEndpoint`, `:330`).
- `Downloaded` (`internal/embed/embed.go:159`) is true only when the config, a
  tokenizer and the weights are all present — the directory existing is not
  enough.

## Web search and fetch

`websearch.Search` (`internal/websearch/websearch.go:39`) calls LangSearch
(`https://api.langsearch.com/v1/web-search`, overridable via `endpoint` for
tests) with `maxResults = 20` (`:26`) — the API accepts more, but every result is
tokens in the transcript and nobody reads the tail of a fifty-result list. The
key is a **per-user setting**; with no key the tool is not offered to the model at
all, so a turn can never spend a call discovering it is unconfigured.

`fetch_url` runs server-side, so it carries both a scheme/address blocklist and a
content-type allow-list:

- `validateFetchURL` (`internal/agent/tools.go:1859`) accepts only `http`/`https`,
  resolves the host and refuses the fetch with `BLOCKED_HOST` when any resolved
  address is loopback, link-local (unicast or multicast), multicast or
  unspecified (`bannedIP`, `internal/agent/tools.go:1888`). Those addresses would
  reach the v1 instance itself or another tenant.
- A DNS failure does not block: the fetch reports it.
- `allowedContentType` (`internal/agent/tools.go:1897`) passes `text/*` and the
  text-ish `application/*` types, refuses PDFs, images, archives and binaries
  (they cannot be meaningfully fed to the model), and allows a missing header —
  the body is then sniffed.

## Sanitisation: the invariant

`internal/sanitize` is a **leaf package** — `agent`, `llm` and `store` all import
it — so every path into a provider (a fresh turn, a retry, or a history stored
before the scrubber existed) passes through the same rules
(`internal/sanitize/sanitize.go:1-4`). That structure is the guarantee: there is
no second route to an outbound request that skips it.

`Text` (`internal/sanitize/sanitize.go:132`) does, in order:

1. strip ANSI OSC and CSI escape sequences;
2. replace invalid UTF-8 with U+FFFD;
3. drop C0/C1 control characters and DEL, keeping `\n`, `\t` and `\r\n` (a lone
   `\r` is removed as a legacy line ending);
4. **redact credentials last**, so redaction sees the final text.

Redaction itself (`redact`, `:114`) has two tiers, and the first is the one that
actually works:

- **Literal values this process holds.** `SetSecrets` (`:74`) replaces the whole
  set at startup (`cmd/v1/main.go:55`), `AddSecret` (`:96`) adds credentials
  learned later (`internal/server/server.go:646,667`). This is the only reliable
  defence: pattern matching cannot catch an arbitrary secret, but knowing the
  value can — an agent may read one from anywhere (a project's `.env`,
  `/proc/1/environ`) and whatever it prints is persisted and sent to a provider.
  Values shorter than `minSecretLen = 8` are ignored.
- **Credential formats.** `credentialPatterns` (`:34-45`) — OpenAI/Anthropic/
  Google/AWS/GitHub/Slack/Stripe keys and a PEM header — is a single list shared
  by redaction *and* by the source secret scan (`agent.scanForSecrets`), so the
  two can never drift apart. A whole PEM block is matched before the header-only
  pattern, which would otherwise replace just the header and leave key material in
  the text (`privateKeyBlockRe`, `:56`).

Where it is applied: `internal/llm/llm.go:217-219` and `:238-254` scrub message
content, reasoning, tool names and tool ids on the way out; `internal/store/store.go:383-384`
scrubs content and attachments on the way in, so history written by an older build
is clean by the next read; `internal/server/harness_chat.go:117`,
`internal/agent/background.go:43`, `handlers_push.go:155` and
`handlers_debug.go:48` scrub text that arrives from the sidecar, a background job,
a push subscriber or a client error.

**Guard rails never to weaken:**

- Sanitisation is applied at the boundary, in a leaf package, for **every** path
  — never in one handler that a future path can bypass.
- Literal-secret redaction is not optional. Pattern lists are a backstop, not the
  defence.
- `<private>` stripping happens on the way **into** memory, and an unclosed tag
  strips to end-of-text.
- `fetch_url` blocks loopback/link-local/multicast/unspecified addresses **after**
  DNS resolution, not by matching hostnames.
- The native encoder stays pure Go: adding cgo would break the `CGO_ENABLED=0`
  build the Dockerfile and CI both rely on.
- The credential pattern list stays shared between redaction and source scanning;
  duplicating it lets the two disagree.

## Related

- [Agent tools, tool results and events](./agent-tools-and-events.md) — the
  `remember`, `search_memories`, `forget`, `web_search` and `fetch_url` tools.
- [Store, sessions and settings](./store-sessions-and-settings.md) — where
  memories, their vectors and the embedding settings live.
- [LLM providers, models and plan usage](./llm-providers-and-models.md) — the
  OpenAI-compatible client whose outbound messages are scrubbed here.
