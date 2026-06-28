# Terminal TUI — multi-session, slash commands, visual feedback

Date: 2026-06-26
Status: approved (design)

## Problem

Launching `herrscher` on a TTY opens the terminal TUI (the `contracts.Foreground`
gateway), captures input, but never replies. Root cause: `RunHub` only starts a
session driver for sessions already in state (`serve.go` iterates
`SnapshotSessions()`); a fresh process has no session bound to the terminal
channel, so typed lines are read by nobody — no `claude` backend turn runs.

Beyond fixing the silence, the terminal should become a usable multi-session
chat client: a left sidebar of sessions, slash commands to create/rename/close,
keyboard navigation, and clear "is it thinking / replying" feedback. Each session
is a real herrscher session (supervisor + bridge + worktree + persisted state),
identical to a Discord session, so it survives restarts and shows in
`session list`.

Auth is already correct: the backend inherits the environment and lets the
`claude` CLI use the user's subscription (no API key forced). See
[[claude-backend-auth-subscription]].

## Key architectural finding

`contracts.Event` has no channel field and `EventSink.Emit(Event)` is unrouted.
The per-session `sessionDriver` knows its channel (`d.channel`); for non-EventSink
gateways it routes via a `gatewayRenderer` carrying that channel, but for
EventSink gateways it just calls `sink.Emit(e)`. Discord gets away with this
because each gateway instance has a channel baked in (`DefaultChannel()`) — which
is also why multiple Discord sessions all post to the same channel today. The
**inbound** path (`Read(channel, …)`) is already channel-aware; only the
**outbound** (`Emit`) loses the channel.

## Chosen approach

One shared terminal gateway instance that multiplexes by channel, plus an
additive `Channel` field on `contracts.Event` for outbound routing.

Alternatives rejected:
- **One gateway instance per session** — faithful to Discord, but the hub binds
  one instance per kind from a global set; making it per-session is invasive.
- **Multiple TUI processes** — breaks the single-screen model.

The driver stamps `e.Channel = d.channel` before fan-out. Discord ignores the
field (keeps using `DefaultChannel()`); the terminal routes by it. The terminal
inbound path is already channel-aware. Net: a small additive contracts bump
(v0.1.7), one host change, no discord-gateway change.

## Phasing

Delivered in two phases so it replies as early as possible.

### Phase 1 — "it replies" + mono-session polish

No contracts change (one session = one pane; unrouted `Emit` suffices).

- `plugins/terminal/terminal.go` — implement `contracts.SessionControlReceiver`:
  store the hub's `SessionControl`, signal "ready". Stays mono-channel.
- `serve.go` / `RunForeground` — on TUI start: wait until `SessionControl` is
  bound (or `ctx` cancelled), then ensure a default session exists (if no
  terminal session is live → `ctrl.Dispatch(["session","create", … workspace +
  dedicated worktree])`), then run the TUI. The core stays gateway-agnostic: the
  terminal self-bootstraps, the host does not special-case it.
- `plugins/terminal/tui/tui.go` — visual feedback: a `· thinking…` status line
  from submit until the first event; stream chunks live (already present); final
  reply + cost (already present).

### Phase 2 — multi-session

- `herrscher-contracts` v0.1.7 — add `Channel string` to `Event` (omitempty,
  additive).
- `core/host/turnloop.go` — `fanOut` stamps `e.Channel = d.channel` before
  `sink.Emit` (and before the renderer branch). Only host change for routing.
- `core/internal/manager` + registry — new `session rename` verb (reused by
  Discord).
- `plugins/terminal/terminal.go` — multi-channel: `panes map[string]*pane`,
  `EnsureChannel` mints a unique id per session (`term:<name>`), `Read` drains
  per channel, `Emit` routes by `e.Channel`, `Submit(channel, text)`.
- `plugins/terminal/tui/` — split into `sidebar.go`, `transcript.go`,
  `input.go`, `commands.go` (slash parser → `Dispatch`). The `model` holds
  `map[channel]*sessionState`, a focus index, and the navigation keys.

## Components

### Terminal gateway (`plugins/terminal/terminal.go`)

- Phase 1: `SessionControlReceiver` with a ready signal; mono-channel.
- Phase 2: per-channel `panes` (own pending inbound + render stream).
  `EnsureChannel` forges `term:<name>`. `Read(channel,…)` drains that channel.
  `Emit(e)` routes by `e.Channel`. `Submit(channel, text)` targets the focused
  session.

### TUI (`plugins/terminal/tui/`)

- `model`: `map[channel]*sessionState` (transcript lines, unread flag, in-flight
  flag), focus index, sidebar focus toggle.
- `commands.go`: anything starting with `/` is a command → argv → `ctrl.Dispatch`;
  otherwise a message → `Submit`.
- Sidebar reconciled from `ctrl.Sessions()` filtered to the terminal gateway, on
  a tick and after each command. No fragile create/close callbacks.

### Host (`core/host`, `core/internal/manager`)

- `fanOut` stamps `e.Channel`.
- `session rename` verb.

### Contracts

- `Event.Channel string` (additive).

## Data flow (one multi-session turn)

1. Focused on session S, user types → TUI calls `terminal.Submit(S.channel, text)`.
2. Terminal enqueues into `panes[S.channel].pending`.
3. S's driver polls `Read(S.channel)` → input frame → bridge → `claude` backend
   (subscription).
4. Backend streams events → `fanOut` stamps `e.Channel = S.channel` →
   `terminal.Emit(e)` → routed to `panes[S.channel]` → TUI updates S's transcript
   (unread marker if S is not focused).
5. `/new` → `ctrl.Dispatch(["session","create",…])` → hub creates
   session+worktree+bridge, `EnsureChannel` forges the channel, `goLive` starts
   the driver. Sidebar picks it up on the next reconcile.

## UX

Layout — sidebar left, transcript + input right:

```
┌────────────┬─────────────────────────────────────────┐
│ SESSIONS   │  herrscher · default                     │
│ › default  │  you  allo                               │
│   api-fix ●│  · thinking…                             │
│   docs   ◦ │  claude  Salut ! …(stream)…              │
│            │  $0.0031                                  │
│            ├─────────────────────────────────────────┤
│            │ > type a message…  (/help)               │
└────────────┴─────────────────────────────────────────┘
```

- `›` focused session; `●` turn in flight; `◦` unread (events arrived off-focus).

Slash commands (parsed in the input; `/`-prefixed = command, else message):
- `/new [--project <path>]` — create a session (dedicated worktree; `--project`
  git-aware autocomplete when git is present).
- `/rename <name>` — rename the current session.
- `/close` — archive the current session + worktree (with confirmation).
- `/sessions` — list/focus sidebar; `/help` — inline help.

Navigation: `Ctrl+↑/↓` (and `Ctrl+J/K`) cycle sessions; `Ctrl+1..9` jump direct;
`Tab` toggles input↔sidebar focus; `Esc`/`Ctrl+C` quits (tears the daemon down).

Feedback: on submit, `· thinking…` until the first event; `status` events faint;
`chunk` streamed live; final `reply` + cost; `abandoned`/`reset` marked.
Off-focus sessions mark unread (`◦`), cleared on focus.

## Worktrees

Every session always gets its own worktree in a dedicated workspace, regardless.
`/new` does not prompt for a project by default; `--project <path>` (git-aware
autocomplete when git is installed) selects an existing repo.

## Testing

- Terminal gateway: per-channel `Submit`/`Read` isolation; `Emit` routes by
  `Channel`; `EnsureChannel` mints unique ids; default session auto-created when
  none.
- Host: `fanOut` stamps `e.Channel`; `session rename` (create → rename → list).
- TUI: slash parser (argv, `--project`); sidebar reconcile from `Sessions()`;
  feedback render (`thinking…`, unread), mirroring the existing `TestRenderEvent*`.
- Contracts: additive field, JSON round-trip only.

## Out of scope (YAGNI)

Persistent per-session scroll-back beyond the in-memory buffer; sidebar resize;
themes; search; multiple windows; rewriting Discord's routing (the `Channel`
field is available to it later).
