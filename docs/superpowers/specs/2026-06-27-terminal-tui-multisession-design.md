# Terminal TUI Multi-Session — Design

**Status:** approved (design) — pending implementation plan
**Date:** 2026-06-27
**Scope:** `herrscher-contracts` (workspace-local), `core/host`, `core/internal/manager`, `plugins/terminal`

## Goal

Drive **multiple concurrent agent sessions from one local terminal**, with no Discord
required — the terminal equivalent of Discord's multi-channel session model. The TUI
presents sessions as **tabs**: one session is visible at a time, the others stream in
the background with an unread indicator. Sessions are created and managed with **slash
commands inside the TUI** (`/session create …`, `/session list`, `/session close`),
reusing the existing operator CLI registry.

Non-goals: split-pane / simultaneous rendering of multiple sessions; a Discord-free
*backend*/*memory* story (unchanged); any new wire transport.

## Background — how the relevant pieces work today (verified)

- **Foreground mode.** `herrscher serve` on an interactive TTY detects the first
  gateway implementing `contracts.Foreground` (the terminal) and runs its TUI on the
  **main thread**, while `RunHub` supervises sessions in a background goroutine
  (`serve.go:75-110`). The raw (undegraded) gateway is captured for `RunForeground`;
  the `gws` slice the hub drives holds the **`Degrade`-wrapped** gateway
  (`serve.go:78-83`).
- **Per-session loop.** Each session runs `RunSession(ctx, name, channel, bound, …)`
  (`core/host/turnloop.go:298`). It polls **its own channel** via
  `Reader.Read(ctx, channel, …)` (~50ms) and fans every turn event out with `fanOut`
  (`turnloop.go:260`), which has the session's `name` and `channel` in scope.
- **Event routing gap.** `contracts.Event` carries **no session/channel id**. The
  terminal gateway is a single shared `*Terminal` bound to the fixed channel
  `"terminal"` (`plugins/terminal/terminal.go:20`). With one session this is fine;
  with several, the shared instance cannot tell which session an `Emit` belongs to.
  **This is the core problem.**
- **`Degrade` strips `EventSink`.** `degrading` (contracts `degrade.go`) forwards only
  `Manifest/Post/Reply/React/Menu/BindSessionControl` — it has **no `Emit`**, so a
  degraded gateway does **not** satisfy `contracts.EventSink`. Consequently `fanOut`'s
  `g.Gateway.(contracts.EventSink)` assertion fails on the wrapped terminal, and the
  daemon path currently falls back to the renderer (final reply via `Post`) rather
  than streaming live `chunk`/`status` events. (Latent gap; fixed here.)
- **Session creation is Discord-shaped.** `sessionCreateRun` (`core/internal/manager/session.go:46`)
  requires `st.Home` to be set, branches on `home.Type` ∈ {`category`,`forum`}, and
  mints the channel via `h.d` (the `firstAdmin` `ChannelAdmin`, `serve.go:185,287`).
  Output uses Discord mention syntax (`<#…>`, `<@…>`). Terminal binding already exists
  conceptually: `ParseGateways` (`gateways.go`) maps `--gateways terminal` /
  `--terminal_only` → `["terminal"]`; the unspecified default is `["discord"]`.
- **Runtime creation goes live immediately.** `SessionControl.Dispatch → reconcile →
  goLive → RunSession` (`hub.go:104,46,59`). A session created in the TUI begins being
  polled (`Read`) and streamed (`fanOut`) at once, provided the terminal is in its
  bound gateways.
- **Session enumeration.** The hub implements `contracts.SessionControl` with
  `Dispatch` and `Sessions() []SessionInfo{Name, ChannelID, Type, Gateways}`
  (`hub.go:104,114`). A gateway receives it via `SessionControlReceiver.BindSessionControl`
  (`serve.go:200-203`).

## Core architecture decisions

1. **New optional capability `RoutedEventSink` (contracts).**
   ```go
   // RoutedEventSink is an optional gateway capability for a gateway that renders
   // more than one conversation's live stream itself (the multi-session terminal
   // TUI). The hub prefers it over EventSink and tags each event with the session's
   // Conversation so the gateway can demultiplex by destination.
   type RoutedEventSink interface {
       EmitTo(conv Conversation, e Event)
   }
   ```
   `fanOut` prefers `RoutedEventSink` over `EventSink`, passing
   `Conversation{Gateway: g.Gateway.Manifest().Kind, ID: d.renderChannel(g)}`.
   A gateway implementing only `EventSink` (or neither) is unchanged. Discord is
   unaffected (it renders through the `Gateway` port, not a sink).

2. **`Degrade` forwards the sink capabilities.** Add `Emit` and `EmitTo` passthrough to
   `degrading` so the wrapped gateway still satisfies `EventSink` / `RoutedEventSink`
   in `fanOut`. As a side benefit this restores live `chunk`/`status` streaming for the
   single-session terminal too.

3. **Terminal becomes a real multi-channel gateway.**
   - Per-channel inbound queues: `Read(ctx, ch, …)` drains only that channel's pending
     lines (replacing the single `pending` slice).
   - `Submit(ch, text)` tags the line with the **active tab's** channel.
   - `EmitTo(conv, e)` routes the event to the tab keyed by `conv.ID`.
   - Implements `ChannelAdmin` so `firstAdmin` can mint synthetic channels
     (`terminal/<name>`) when no Discord gateway is present; `Archive` drops the tab,
     `Send` posts a system line into the tab, `CreateUnder`/`Kind` are terminal-local.

4. **Frontend carries the channel.** The `tui.Backend` seam changes from
   `Frontend() <-chan Event` + `Submit(text)` to a routed form:
   ```go
   type RoutedEvent struct { Conv contracts.Conversation; Event contracts.Event }
   type Backend interface {
       Frontend() <-chan RoutedEvent
       Submit(channel, text string)
       Sessions() []contracts.SessionInfo // tab labels/order; empty pre-bind
   }
   ```
   The TUI demultiplexes `RoutedEvent`s into per-tab buffers and owns all tab state.

5. **Session list is the source of truth for tabs.** The terminal implements
   `SessionControlReceiver`, stores the `SessionControl`, and uses `Sessions()` for tab
   **labels and order** (names). A channel seen via `EmitTo`/`Read` with no matching
   session is shown with its channel id as a fallback label and reconciled on the next
   `Sessions()` refresh.

6. **Terminal-only session creation (generalize the handler).** Make
   `core/internal/manager` gateway-agnostic rather than Discord-specific:
   - Create the channel through the injected `ChannelAdmin` (`h.d` = `firstAdmin`)
     regardless of platform; support a `home.Type == "terminal"` home.
   - Replace Discord-only mention syntax in user-facing strings with neutral text (or
     keep Discord rendering behind the Discord gateway, not the core handler).
   - Seed/allow a terminal home so `home.ID != ""` passes with no Discord configured.
   - In foreground (terminal) mode, default new sessions to bind the terminal gateway
     (so `/session create` without `--gateways` yields a terminal tab); `--terminal_only`
     remains the explicit shorthand.

## Component boundaries

- **`herrscher-contracts`** — adds `RoutedEventSink`; `Degrade` forwards `Emit`/`EmitTo`.
  No change to the `Event` wire shape. (Workspace-local edit via `go.work`; tag bump
  handled at integration.)
- **`core/host`** — `fanOut` prefers `RoutedEventSink`. No other behavior change.
- **`core/internal/manager`** — session create/close/list made platform-agnostic; a
  `terminal` home type supported; neutral user-facing strings.
- **`plugins/terminal`** — gateway gains per-channel queues, `EmitTo`, `ChannelAdmin`,
  `SessionControlReceiver`; `Submit(channel,text)`.
- **`plugins/terminal/tui`** — multi-tab model: tab list, active tab, switch keys,
  unread/activity indicators, per-tab transcript, slash-command dispatch.

Each unit keeps a single purpose and a narrow interface: the host stays
gateway-agnostic (it only learns `Conversation`), the TUI never imports the terminal
plugin (drives the `Backend` seam), and the manager never learns a platform.

## Phasing (1 → 3)

### Phase 1 — Routing plumbing + multi-tab rendering
Make events route correctly to per-tab panes; sessions still created via the existing
CLI (`herrscher session create --gateways terminal …`).
- contracts: add `RoutedEventSink`; `Degrade` forwards `Emit`/`EmitTo`.
- host: `fanOut` prefers `RoutedEventSink.EmitTo` with the session `Conversation`.
- terminal gateway: per-channel inbound queues; `EmitTo` demux; `Submit(ch,text)`;
  `Frontend` yields `RoutedEvent`.
- TUI: tab model (channel → pane), render active tab, `Tab`/`Shift-Tab` to switch,
  unread marker. Tabs discovered from `EmitTo`/`Read` channel ids.
- **Exit:** sessions bound to terminal appear as live tabs that stream `chunk`/`status`/`reply`
  to the correct pane; switching tabs works; the single-session path still works.

### Phase 2 — Create/manage sessions from the TUI
- terminal: implement `SessionControlReceiver` (store control), dispatch a typed `/…`
  line through `SessionControl.Dispatch`; implement `ChannelAdmin` (synthetic
  `terminal/<name>` channels, archive→close tab, send→system line).
- core/internal/manager: generalize channel creation through `firstAdmin`; support a
  `terminal` home; neutral strings; default-bind terminal in foreground mode.
- TUI: `/session create|list|close` create/close tabs live; tab labels from `Sessions()`.
- **Exit:** with no Discord configured, `herrscher serve` on a TTY lets the operator
  create, switch between, and close multiple sessions entirely from the TUI.

### Phase 3 — UX polish
- session switcher / command palette; activity (streaming) + unread indicators;
  per-tab status/cost line; close-tab with confirm; per-tab scrollback; keybinding
  help; graceful handling of a session whose bridge dies; optional "home" overview tab
  listing sessions.
- **Exit:** the multi-session TUI is comfortable for daily multi-agent operation.

## Testing strategy

- **contracts:** unit test that `Degrade(g)` satisfies `EventSink`/`RoutedEventSink`
  when `g` does, and forwards calls; `fanOut` prefers `RoutedEventSink` (host test with
  a fake gateway recording `EmitTo` conversations).
- **terminal gateway:** `Read` drains per-channel; `Submit(ch,…)` tags the channel;
  `EmitTo` routes to the right tab buffer; `ChannelAdmin.CreateUnder` mints unique ids;
  `SessionControlReceiver` stores and forwards `Dispatch`.
- **tui:** table-driven `Update` tests — `RoutedEvent` lands in the right tab, switch
  keys change active tab, unread clears on focus, a `/session …` line dispatches rather
  than submits as a prompt. Existing `renderEvent` cost/abandoned tests are preserved
  per-tab.
- **manager:** session create works with a `terminal` home and no Discord; default
  gateways include terminal in foreground mode; user-facing strings are platform-neutral.
- **purity:** the host/core must still import zero platform code (existing `purity_test.go`).

## Risks & mitigations

- **`Degrade` passthrough is load-bearing.** If `fanOut` runs against the wrapped
  gateway and the passthrough is missing, multi-session streaming silently degrades to
  final-reply-only. Mitigation: explicit contracts + host tests asserting the wrapped
  gateway satisfies the sink and that `EmitTo` is the path taken.
- **Backend seam change ripples.** Changing `tui.Backend` (`Submit`/`Frontend`
  signatures) touches the gateway↔TUI boundary and existing tests. Mitigation: land the
  seam change and its tests together in Phase 1.
- **Manager generalization could regress Discord.** Mitigation: keep Discord behavior
  via the same `ChannelAdmin` path and a `category`/`forum` home; cover both home types
  in handler tests; neutral strings verified to still render acceptably in Discord.
- **Tab/session drift.** A session closed elsewhere (CLI) must remove its tab.
  Mitigation: reconcile tabs against `Sessions()` on a refresh and on `Archive`.
