---
type: "Reference"
title: "Web SPA architecture"
openwiki_generated: true
verified:
  - by: openwiki/0.9.0
    at: 2026-11-15T05:31:58.285Z
sources:
  - id: openwiki-source-0e9061c0550ceeed2cb5e23b
    resource: repo://web/index.html
  - id: openwiki-source-4f8faebc6d9f61c678cec0e8
    resource: repo://web/src/api.ts
  - id: openwiki-source-2a66ee6d8055e80ef6e42df6
    resource: repo://web/src/App.tsx
  - id: openwiki-source-804d535bd77653c5ebd32179
    resource: repo://web/src/appearance.ts
  - id: openwiki-source-bdac1941a583a38762a06fdb
    resource: repo://web/src/components/ChatPane.tsx
  - id: openwiki-source-0c8d65b7dce46d10a3f5bcd3
    resource: repo://web/src/components/ChatPanel.tsx
  - id: openwiki-source-2d994c2a65621110d714e307
    resource: repo://web/src/index.css
  - id: openwiki-source-fd49d937521d699f0ccd7a77
    resource: repo://web/src/main.tsx
  - id: openwiki-source-502024de1e9de37d46b947dd
    resource: repo://web/src/pages/Project.tsx
  - id: openwiki-source-7197083210028bf301050bf4
    resource: repo://web/src/themes.ts
  - id: openwiki-source-8e8663cb08e3f377bcf8f95f
    resource: repo://web/src/utils.ts
generated: { by: "v1", at: "2026-11-15T05:31:58.285Z" }
---

# Web SPA architecture

`web/src/` is a React single-page app that the Go server embeds and serves as
static files (`internal/server/server.go:1126`). It owns no state of record: the
transcript, the queue, the settings and the appearance all live server-side, and
the SPA's job is to render them, stream a turn as it happens, and converge on
what was persisted.

Three patterns recur and explain most of the code:

- **The mirror.** Anything a render path needs synchronously (theme, chat side,
  tab order, composer order) is read from `localStorage` through
  `web/src/utils.ts`, and the server copy is applied over it later. A
  network round trip never sits between a click and a pixel.
- **Parity between live and reloaded.** A row built from an SSE event is built
  in exactly the shape the persisted row maps to, so a reload cannot re-style,
  re-order or re-count it.
- **Converge on the server.** Optimistic UI is a placeholder; the end of a turn
  re-reads the real transcript instead of patching the placeholder into truth.

The HTTP surface it talks to is [HTTP API and auth](../architecture/http-api-and-auth.md);
the stream it consumes is [Chat turns, queue and steer](../architecture/chat-turns-and-queue.md);
the layout knobs it reads are [Store, sessions and settings](../architecture/store-sessions-and-settings.md).

## Boot

`web/src/main.tsx` (63 lines) runs in a deliberate order:

1. `initTheme()` (`web/src/main.tsx:9`) **before** `createRoot().render` — the
   theme is a set of CSS variables on `document.documentElement`, and applying it
   after the first paint would flash the default palette.
2. `installDebugLog()` (`:13`).
3. `void hydrateAppearance()` (`:19`) — the account's appearance lands in the
   background; anything already rendering re-reads it through
   `subscribeAppearance`.
4. Service-worker registration, production only (`:25`), plus a
   `controllerchange` listener that reloads exactly once (`:33`) with the reason
   recorded: "a PWA that is left open and resumed keeps executing the bundle it
   was loaded with". The worker is registered with `updateViaCache: 'none'` and
   poked with `reg.update()` (`:40`), and re-poked on `visibilitychange`
   (`:46-50`).

`web/index.html` carries a pre-paint inline script (`web/index.html:20-95`) that
persists the tallest plausible viewport height per orientation under
`v1-app-height-v2:<portrait|landscape>` and writes `--v1-app-height`. It exists
because iOS standalone mode reports a stale inner height; heights are capped so a
bogus reading cannot be stored forever (the comment at `:21-34` notes that the
older un-suffixed keys *did* hold such a value, hence `v2`).

## Routes and the auth gate

`web/src/App.tsx` (105 lines) uses `react-router-dom`
(`web/src/App.tsx:2`) — a real router, not a hand-rolled hash switch.

| Path | Page |
| --- | --- |
| `/login` | Login (`web/src/App.tsx:73`) |
| `/setup` | first-run admin creation (`:74`) |
| `/` | dashboard, `Projects` (`:76-82`) |
| `/project/:id` | `Project` — chat + preview (`:84-90`) |
| `/settings` | `Settings` (`:92-98`) |
| `*` | `<Navigate to="/" replace />` (`:99`) |

`RequireAuth` (`web/src/App.tsx:15`) wraps everything except the two auth routes.
It calls `api.getAuthStatus()` (`:22`) and renders three non-page states before
content: a "Cannot reach the server" box with Retry on failure (`:43-56`), a
`Spinner` while the status is unknown (`:57-63`), then redirects —
`status.setupRequired` → `/setup` (`:64`), and
`status.authRequired && !status.authenticated` → `/login` (`:65`). The dashboard
is therefore never rendered for an unauthenticated reader, which is what makes
the auth-disabled deployment behave identically to the authenticated one from
the SPA's point of view.

`<ToastHost />` is rendered outside `<BrowserRouter>` (`web/src/App.tsx:101-104`)
so a toast survives navigating away from the page that raised it.

## The API client

`web/src/api.ts` (1122 lines) is a single object literal plus a few helpers. Two
things in it are load-bearing beyond plain `fetch` wrappers.

### `request` and its error contract

`request<T>(path, init)` (`web/src/api.ts:150`):

- `credentials: 'same-origin'` on every call (`:152`) — the session is a cookie,
  not a bearer token.
- A thrown `fetch` is logged through `debugError('fetch', …)` and rethrown
  (`:154-161`), so a transport failure stays distinguishable from an HTTP error.
- `401` redirects to `/login` and throws `ApiError('Unauthorized', 401)`
  (`:162-165`); `redirectToLogin()` (`:133`) is a no-op when the reader is
  already on `/login` or `/setup`.
- Any other non-2xx throws `ApiError(await parseError(res), res.status)`
  (`:166-173`), where `parseError` (`:140`) prefers the body's `error` field,
  then `statusText`, then `Request failed (<status>)`.
- `204` returns `undefined` (`:178`), and a body is parsed as JSON only when the
  content type says so (`:179-183`).

There is no token refresh and no offline queue anywhere in the client: the
session is a 30-day cookie, and a failed write is reported, not replayed.

### Caches, all in module scope

| Cache | TTL | Constant |
| --- | --- | --- |
| provider catalog (memory + `localStorage`) | 6 h | `PROVIDERS_TTL` (`web/src/api.ts:60-62`) |
| context usage | 10 s | `CTX_USAGE_TTL` (`:88`) |
| plan usage | 60 s | `USAGE_TTL` (`:95`) |

`clearClientCaches()` (`:106`) drops those caches after sign-out and says
explicitly that "Settings and preferences are not touched" (`:102-105`);
`expireProvidersCache()` (`:123`) drops only the catalog, and is called on any
settings write that could change it. The plan-usage TTL carries its own
rationale (`:92-94`): the composer is re-rendered constantly, and the provider's
rate-limit endpoint should not be polled per render.

### SSE streaming and buffered-stream detection

`streamChatEvents(path, body, onEvent, signal, get = false, idleMs = STREAM_IDLE_MS)`
(`web/src/api.ts:771`) is shared by `streamChat` (`:995-1033`, `POST /chat`),
`retryChat` (`:1035-1071`) and `watchChat` (`:1076-1093`, `GET /chat/watch`).
Four decisions in it matter:

- **It owns an inner `AbortController`** (`:782`) that follows the caller's
  signal (`:777-781`), so the stream itself can end the request.
- **Two different deadlines.** `STREAM_IDLE_MS = 45_000` (`:707`) is how long a
  *started* stream may go silent — the server writes an SSE comment every 15s,
  so silence at this scale means the connection is gone, not that a tool is slow
  ("a half-open socket never errors, so the reader simply waited").
  `STREAM_FIRST_EVENT_MS = 15_000` (`:716`) is the deadline *before* the first
  event, and it races the `fetch` itself (`:794-795`) because a proxy may hold
  back headers just as easily as it holds back the body ("Headers can be held
  back too"). Missing that deadline rejects `BufferedStreamError` (`:724`).
- **`202` is not a stream.** A send into a session that already has a run is
  answered with JSON, and the client throws `QueuedError` carrying the queue id
  (`:739`, thrown at `:825-833`). The comment at `:727-738` records what reading
  that JSON as SSE cost: no events, then a synthetic `done` — "notified
  \"finished\" for a chat that was still working, put the composer back to idle,
  and left the message pinned to the bottom of the transcript".
- **A synthetic `done` at the tail** (`:887-890`) so a caller that got events but
  never a `done` frame still finishes. The idle branch (`:866-874`) cancels the
  reader and throws `BufferedStreamError` only if no event ever arrived.

Frames are split on blank lines (`:878-884`), `data:` prefixes are stripped and
joined with newlines (`:849-866`), and a frame that does not parse as JSON is
dropped rather than failing the stream. `readWithIdleDeadline(reader, ms)`
(`:750`) implements the `Promise.race` every wait goes through.

`watchChat` uses a shorter `WATCH_IDLE_MS = 20_000` (`:1074`): the watch stream
exists only to deliver pings and queued follow-ups, so "a dead socket is
recognized after one missed ping instead of three".

### Uploads are not base64 in the turn

`uploadAttachment` (`web/src/api.ts:933`) is the only `XMLHttpRequest` in the
client, kept for upload progress (`:925-932`). By the time the reader presses
send the bytes are already on the server, so the turn carries an upload id
instead of megabytes of base64 — "which is what used to lose a message when the
connection dropped mid-upload".

## ChatPane: the transcript model

`web/src/components/ChatPane.tsx` is 9474 lines. Its shape: item types at module
scope (`:64-3748`), the component at `:4157`, its hooks and derived data from
`:4267` to `:8517`.

### Three item kinds

```ts
type MsgItem  = { kind: 'msg'; role: 'user' | 'assistant' | 'error'; … }  // :530
type ToolItem = { kind: 'tool'; name: string; call?: string; … }         // :567
type AskItem  = { kind: 'ask'; questions: …; result?: unknown }          // :584
type Item     = MsgItem | ToolItem | AskItem                             // :592
```

`MsgItem` covers a persisted message *and* a live one, which is why the
per-message flags live on it: `background` / `notice` (`:543-547`),
`steered` / `injected` (`:557-561`), plus `usage`, `elapsedMs`, `streaming`,
`stale` and `editing`. `ToolItem.call` holds "the call's own arguments (a path, a
command) as `tool_start` reported them", which is what lets a chip show something
useful before the result exists.

### Who wrote a row

The rule is a single predicate, not a scattering of role checks:

```ts
const USER_WRITTEN_MARKERS = new Set(['', 'steer']);   // ChatPane.tsx:502
function writtenByUser(marker: string): boolean        // :503
```

The comment above it (`:492-501`) is the design statement: "only the markers
that mean 'they wrote this' belong [on the right]; anything unrecognised is the
app's, which is what keeps a marker added later from silently landing on their
side". `markerName(raw)` (`:518`) normalises both shapes a marker can arrive in —
the persisted bare `steer` and the live JSON text `"steer"` — so "one decoding
rule on the way in means the shape never decides who wrote a row" (`:506-517`).

The marker semantics themselves are server-side and documented in
[Store, sessions and settings](../architecture/store-sessions-and-settings.md):
`''` is the reader, `steer` is the reader steering mid-turn, `agent` and
`background` are the app.

### `mapMessages`: one row, one item

`mapMessages(msgs, startUserAt)` (`:4916-5021`) is the only place a stored row
becomes UI:

- `role === 'tool'` attaches to the previous assistant item's `toolResults`, or
  starts an empty assistant item when there is none (`:4928-4948`) — with the
  noise filter `if (isNoiseResult(m.content)) continue;` (`:4926`).
- `role === 'assistant'` (`:4950-4957`) also computes
  `elapsedMs = (Number(m.createdAt) - lastUserAt) * 1000` on a turn-final row
  (`:4959-4962`), and `parseToolCalls(m.tool)` keeps only the first `set_todos`
  and drops `list_files` with no meaningful detail (`:4964-4976`) — "set_todos
  repeats are pure progress noise".
- `role === 'user'` (`:4991`) sets `background`, `steered` and
  `injected: !writtenByUser(marker) && !background`, then gives every attachment
  a URL (`:4994-5003`) — images *and* text, so a text attachment opens too.
- `role === 'notice'` (`:5004-5015`) becomes an assistant item with
  `notice: true`; the comment notes the server skips these rows when it rebuilds
  history. Anything unrecognised falls through to `role: 'error'`
  (`:5016-5018`) rather than disappearing.

`isNoiseResult(detail)` (`:1350`, defined `:1346-1349`) is true only for
`{ok: true}` with no `error`, `content`, `output` or `results` — an empty success
is not worth a card.

### Live-versus-reloaded parity

The rule: **a live card is built in the reloaded row's shape.** Three places
enforce it.

- `tool_start` appends
  `{ kind: 'tool', key, name, call: ev.detail, detail: ev.detail, running: true }`
  (`:6298-6318`) — the arguments are kept as `call` from the start, exactly where
  a reloaded `ToolItem` would carry them.
- `tool_end` pops `toolStackRef.current[ev.name]` and patches
  `{ running: false, ok, detail: ev.detail || it.detail, display: ev.display ?? it.display }`
  (`:6364-6377`). With no matching start it still builds the card, but inserts it
  **before any trailing tool/ask cluster** (`:6380-6399`) — a plain append is
  what used to put a result card below a summary that had already rendered.
- `injected_message` (`:6601-6661`) reads
  `const marker = markerName(ev.toolJson ?? …)` (`:6620-6622`) and styles from
  it. The comment (`:6616-6619`) gives the failure this fixed: "the screenshot
  caption the harness emits has no `tool` hint, and guessing from one is what put
  it on the reader's side until the next reload", and (`:6624-6626`) "styling
  comes from the marker the server persisted, never from whether a pending steer
  happened to match".

Two smaller parity rules: a live notice row is keyed by the persisted
`ev.messageId` so a reload replaces it in place (`:6495-6513`), and `done`
converges with `void load({ quiet: true }); finish();` (`:6572-6578`) — "converge
on the persisted transcript, exactly like reopening the chat does… without this
the reader had to leave the chat and come back to see it".

Rendering follows the same flags: `item.background` → `BackgroundResultRow`
(`:3879-3881`), `item.injected` → `AgentInjectedRow` (`:3882-3891`), and a steered
row is accent-bordered with no Edit/Rewind affordances (`:3893-3905`) because
rewriting history from the middle of a steer is not meaningful. Tool chips are
`ToolChip` (`:1512`), whose label comes from `chipLabel(detail, display?)`
(`:1369`): it fills `display.summary`'s `{field}` templates first, then falls back
to `command ?? path ?? query ?? url`, then to semantic fields — the same order the
extension contract uses (see
[Extensions and skills](../architecture/extensions-and-skills.md)).

### The per-turn fold

The transcript folds in three nested layers, from inside out:

| Component | Location | Scope |
| --- | --- | --- |
| `CollapsedTools` | `ChatPane.tsx:2506` | one summary line per assistant message |
| `CollapsedToolsGroup` | `:2590` | merges consecutive summaries |
| `TurnFold` | `:2568` | the whole turn's work, "Show the steps in between" |

Each auto-opens while something inside it is still running — `CollapsedTools`
"only collapses once everything inside has completed" (`:2526-2531`) and holds
open as a group so "the whole turn opens and closes together" (`:2517-2521`);
`CollapsedToolsGroup` computes `auto = anyPendingAsk || (active && anyRunning)`
(`:2609`) because "an unanswered question is never hidden behind the toggle"
(`:2605-2608`).

The decision of *what* folds happens in one pass, not in the renderers:
`flushTurn` (`:8493`), with its rule at `:8483-8492`:

```ts
const work = summary != null ? nodes.filter((n) => n !== summary) : nodes;
if (!live && getTurnFoldEnabled() && summary != null && work.length > 0) {
  rows.push(<TurnFold key={`fold-${foldKey}`}>{work}</TurnFold>);
  rows.push(summary);
}
```

So exactly one row per turn stays outside the fold — the turn's final assistant
text — and only once the turn is finished. The comment (`:8484-8487`) explains
both conditions: folding the live turn "split a turn in two (finished rounds
behind the toggle, the live round below it)". The summary row is chosen by
scanning backwards for the last `role === 'assistant' && !background &&
content.trim()` row (`:8419-8434`) — "intermediate comments fold into the
collapse, in order, where they happened" — and re-rendered with reasoning, tool
calls and tool results stripped (`:8440-8446`). `turnEndKeys` (`:7660`) collects
the turn-final rows that carry the usage line; `toolRound` rows never count.

The fold is a preference (`v1.turnFold`, default off) — see
[Store, sessions and settings](../architecture/store-sessions-and-settings.md)
for the account-scoped copy.

### The composer row

```ts
const composerButtons: Record<ComposerButton, ReactNode> = { … }  // ChatPane.tsx:8104
```

The doc comment (`:8100-8103`) states the contract: `null` means "not applicable
right now" and the row drops it. Keys: `thinking` (`:8105`), `context` (`:8123`,
the `ContextRing` at `:392` — 18×18, `R = 7.5`, hue `120 - (120 * pct) / 100`),
`usage` (`:8145`, the three plan-usage rings, disabled when `!usageSupported` and
tinted by the worst window, `:8096-8099`), `permissions` (`:8177`), `tools`
(`:8188`, desktop only), `tasks` (`:8189`), `minimap` (`:8190`).

Order and visibility come from the layout, not from the map's key order:

```tsx
{visibleComposerButtons(composerLayout).map((id) => (
  <Fragment key={id}>{composerButtons[id]}</Fragment>
))}
```

(`:8225`, comment `:8227-8228`): "Order and visibility come from Settings →
Appearance; four is the cap, and the model name takes whatever they leave." The
model button (`:8213-8224`) is `flex-1` with a `truncate` inner span, so it
absorbs exactly the width the buttons do not take.

### Optimistic send and the queue

`sendText` (`:6923`) paints a placeholder user row keyed `u${++counterRef.current}`
with a `data:` URL per attachment (`:6943-6968`) so chips open before anything is
persisted. Finished uploads travel as `uploadIds` and unfinished or inline ones as
`atts`, "so nothing is ever dropped from a send" (`:6936-6941`); a failure rolls
the rows back and restores the text *and* the files (`:6905-6917`).

`send` (`:7215`) steers by default while a stream is running; `queueInput`
(`:7248`) queues without steering. Queue state (`:4301`, `:4316`) is refreshed by
`refreshQueue` (`:4898`) and rendered at `:8605-8710`: `Queued (<n>)` (`:8610`),
reordering only offered when more than one entry exists (`:8659`), plus inline
edit (`:8627`) and delete (`:8700`).

The first-message auto-send has a guard (`:6993-7010`):
`if (loadError || items.length > 0) return;`. It checks history rather than a
ref, because "a browser back restores that state … the history check, not the
ref, is what survives the remount", and without it returning to a finished chat
would re-send the project description.

## The appearance mirror

`web/src/appearance.ts` (158 lines) is the bridge between the account-scoped
settings row and the synchronous render paths. Its header (`:1-10`) is the
contract:

> The mirror is not redundancy — it is what makes the settings usable
> synchronously. The theme has to apply before the first paint, the chat side and
> the tab bar are read during render … reads stay synchronous
> (`localAppearance()`), a change writes the mirror and the server, and the
> owner's values land from `hydrateAppearance()` once the app has a session.

- `localAppearance()` (`:39`) composes the `utils.ts` getters into one
  `AppearanceSettings` object.
- `applyAppearance(patch)` (`:63`) writes the mirror and applies the theme when
  it actually changed (`:64-66`), then notifies every listener (`:96-97`).
- `hydrateAppearance()` (`:114`) is one-shot and idempotent —
  `hydration ??= api.getSettings().then(applyAppearance).catch(debugError)` — and
  a failure "leaves the mirror in place" (`:109-113`).
- `saveAppearance(patch)` (`:149`) is optimistic-first: apply locally, then
  `api.updateSettings({ appearance: patch })`; a failed save is logged, not
  surfaced, because "the mirror still holds the choice for this device"
  (`:152-157`).
- `subscribeAppearance(fn)` (`:31`) is how a component re-reads after the
  account's copy lands; `ChatPanel` (`web/src/components/ChatPanel.tsx:78`) and
  `Project` (`web/src/pages/Project.tsx:75`) both do exactly that.

Two normalisations are deliberate and easy to get wrong. An **empty order** means
"the built-in order", not an empty list: `chatTabs.order` falls back to
`CHAT_TABS` (`:69-76`) and `composerButtons.order` to
`DEFAULT_COMPOSER_BUTTONS` (`:79-95`) — "not 'every button at once'". And
`composerButtons.order` holds **hidden ids too**, so it is the full list, not the
visible four; only `visibleComposerButtons` slices to the cap.

### The composer cap

`web/src/utils.ts` defines the layout and enforces the cap in one place:

```ts
export type ComposerButton = 'thinking' | 'context' | 'usage'
  | 'permissions' | 'tools' | 'tasks' | 'minimap';                    // :297
export const MAX_COMPOSER_BUTTONS = 4;                                 // :325
export const DEFAULT_COMPOSER_BUTTONS = {                              // :330
  order: ['context', 'usage', 'permissions', 'minimap', 'thinking', 'tools', 'tasks'],
  hidden: ['thinking', 'tools', 'tasks'],
};
export function visibleComposerButtons(l: ComposerButtonLayout) {       // :373-375
  return l.order.filter((id) => !l.hidden.includes(id)).slice(0, MAX_COMPOSER_BUTTONS);
}
```

The default ships the row at exactly four (context ring, plan-usage rings,
permissions, minimap) with thinking/tools/tasks one toggle away (`:326-329`).
`getComposerButtonLayout()` (`:337`) appends any button added since the layout was
saved (`:348`) and then **demotes** visible overflow past the cap into `hidden`
(`:350-357`) — "a layout saved before the cap (or edited by hand) can name more,
so the extras are hidden here rather than silently dropped from the far end".
Storage key `v1.composerButtons` (`:335`); the same file holds the tab-bar
equivalent (`v1.chatTabs`, `:263`, with `CHAT_TAB_IDS` at `:265`) and the older
single-value preferences: `v1.chatSide` (`:47`), `v1.jsonPretty` (`:150`),
`v1.toolCallsCollapsed` (`:170`), `v1.turnFold` (`:191`),
`v1.thinkingCollapsed` (`:211`).

`Settings → Appearance` edits both lists with drag, arrows and an eye toggle, and
persists through `saveAppearance` (see
[Store, sessions and settings](../architecture/store-sessions-and-settings.md)).

## Pages and layout

- `web/src/pages/Project.tsx` (240 lines) splits chat and preview with
  `react-resizable-panels` (`:3`). The chat side is
  `useState<'left'|'right'>(() => getChatSide())` (`:72`) re-read on
  `subscribeAppearance` (`:75`): the mirror answers the first render, the
  account's copy lands later. It also owns the mobile pane switch (`:69`),
  `chatCollapsed` (`:70`), `chatResetSignal` (`:72`, bumped by the bottom-nav
  Chat button) and the `DebugHud` (`:17-58`) — the iOS stale-viewport overlay
  behind `v1.debugHud`, which prints inner height, client height, visual viewport,
  a `dvh` probe, the CSS variable and the stored height.
- `web/src/components/ChatPanel.tsx` (265 lines) owns the subtab bar.
  `TABS` (`:31-39`) lists chat/files/terminal/git/memories/github/project;
  `tabLayout` is read the same way (`:75`, `:78`). The GitHub tab is
  conditional (`:93-97`): shown when the project has not loaded yet (`null`),
  when explicitly `on`, or when the repo URL looks like GitHub and the tab is not
  `off` — "a project that has not loaded yet keeps the tab: popping it in a
  moment later is worse than showing it briefly". Tabs are filtered by `hidden`
  then sorted by index in `order` (`:94-97`), `initialTab` is `order[0] ?? 'chat'`
  (`:98`), and a guard effect re-homes the view when the active tab disappears
  (`:113-120`).
- `web/src/pages/Projects.tsx` (1052 lines) is the dashboard — including the
  per-chat archive button and its confirmation dialog. `web/src/pages/Settings.tsx`
  (3719) hosts the appearance sections. `web/src/pages/Login.tsx` (242) covers
  login, setup and signup.

## Theming and the shell

`web/src/themes.ts` (232 lines) builds themes as happyHues palettes and derives
contrast-aware text colours (`deriveFromPalette`, `:143`). `applyTheme(name)`
(`:207`) writes twelve CSS variables onto `document.documentElement.style` from
`VAR_MAP` (`:187`) — `--v1-bg`, `--v1-surface`, `--v1-border`,
`--v1-border-strong`, `--v1-text`, `--v1-text-dim`, `--v1-text-subtle`,
`--v1-text-faint`, `--v1-accent`, `--v1-accent-text`, `--v1-primary`,
`--v1-primary-text` — sets `style.colorScheme` from the palette's luminance and
toggles `v1-theme-light` on the body for the light code-block palette. The default
is `'v1 Dark'` (`:32`), whose values "match the original hardcoded UI colors
exactly" (`:35`); `THEME_STORAGE_KEY = 'v1.theme'` (`:31`).

Tailwind never hardcodes a colour: `web/src/index.css` maps its tokens onto those
variables with `@theme inline` (`web/src/index.css:15-35`), so `bg-bg`,
`text-dim` and `border-border-strong` follow the active theme. `:root`
(`:37-53`) holds the v1 Dark literals, `overscroll-behavior: none` (`:61-63`)
keeps the app shell from bouncing, and `.v1-safe-top` (`:409-416`) pads by
`env(safe-area-inset-top) * 7 / 8` — "Apple's inset is 0 in regular browsers, so
this is a no-op outside standalone mode".

`web/index.html:9` sets the viewport to `viewport-fit=cover,
interactive-widget=resizes-content, maximum-scale=1.0`, and `:12-19` declares
`color-scheme`, `theme-color`, the mobile-web-app meta tags and the manifest link.
`web/public/manifest.json` declares `display: standalone`, `start_url: "/"` and
maskable icons.

## Invariants worth keeping

1. **Never build a row from an event without the marker.** Authorship comes from
   `toolJson`, never from a guess about the content shape (`ChatPane.tsx:6616-6626`).
2. **Never fold the live turn.** The fold waits for the turn to finish, and only
   the final assistant text stays outside it (`ChatPane.tsx:8483-8492`).
3. **Never treat a proceeded stream as a finished turn.** `202` is a queue
   acknowledgement, and a synthetic `done` is a fallback, not a verdict
   (`web/src/api.ts:727-738`, `:887-890`).
4. **Never queue an appearance write behind a fetch.** The mirror is written
   first; the server copy follows and merges back
   (`web/src/appearance.ts:149-157`).
5. **Never exceed four composer buttons, and never let a saved layout exceed
   it.** The cap is applied on read, not only in the editor
   (`web/src/utils.ts:350-357`).
