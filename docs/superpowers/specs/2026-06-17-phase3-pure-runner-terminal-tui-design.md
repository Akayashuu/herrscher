# Phase 3 — Pure-Runner Hub + Terminal Gateway + Bubbletea TUI

**Date:** 2026-06-17
**Status:** Approved (brainstorm), pending implementation plan
**Builds on:** Phase 1 (multi-gateway hub), Phase 2 (structured session bus). See
`2026-06-16-serve-tui-multi-gateway-hub-design.md` for the originating design.

## Goal

`herrscherd serve` opens an in-process Claude-Code-like TUI in the terminal. The
terminal becomes a first-class gateway, peer to Discord. The daemon becomes a
multi-gateway hub that owns all gateway I/O and fans a turn's output out to every
gateway bound to the session. The `bridge` subprocess becomes a pure runner: it
receives input, runs the Claude backend, and emits turn events — it knows nothing
about any gateway.

## Topology decision: Pure-Runner (Option B)

The pivotal Phase 3 fork was **who owns gateway I/O**. Decision: **B, the
pure-runner topology** (not the hybrid where the bridge keeps Discord). Rationale:
the hybrid is a detour we'd later demolish; Phase 2's structured bus was built as
the ramp to pure-runner; the spec already defines the bridge as a pure runner and
the terminal as a Discord peer. We pay the refactor once, now.

**Sequencing:** implement **terminal-first, then Discord**. Stand the pure-runner
up against the terminal gateway alone (a simple, testable local loop), then
re-attach Discord as a second gateway through the hub. This gives the "see the TUI
working fast" benefit without building throwaway code.

```
daemon-hub (serve)                          bridge (subprocess / session)
├─ gateway Discord ─┐                       ┌──────────────────────────┐
├─ gateway terminal ┤─ FIFO ─► input ───────► Claude backend           │
├─ TUI Bubbletea   ─┘◄── events (fan-out) ──┤ emits human/chunk/status/ │
└─ listens socket  ◄──── dial+redial ───────┘ reply                     │
```

### Responsibilities

- **daemon-hub** owns: all gateway I/O (Discord + terminal), the per-session FIFO
  turn queue, the input pump (queue → bridge), and the event fan-out (bridge →
  every bound gateway).
- **bridge** owns: nothing gateway-related. Reads input from the socket, runs the
  backend, emits `control.Event`s. No `Post`/`Reply` to any gateway directly.
- **purity** is preserved: `core`/`host` import no concrete plugin. The terminal
  gateway is a plugin that self-registers via `init()`, exactly like Discord.
  `TestCorePurity`/`TestHostPurity` stay green.

## Turn model: FIFO

Per session the hub holds a FIFO queue of inputs. An input is
`{gateway source, author, text}`.

- One active turn at a time. While a turn streams, new inputs from any gateway
  enqueue.
- End of turn = bridge emitted its terminal `reply{Done:true}`. The hub then
  dequeues the next input and pushes it to the bridge.
- A turn's stream fans out to **all** gateways bound to the session, not just the
  emitting one. (Type in the terminal → Discord sees the reply too, and vice
  versa.)
- **No interruption in the MVP.** Once a turn is sent to the bridge it runs to
  completion. Ctrl-C / preempt is a future feature.

## Transport: persistent bidirectional socket

Rewrite the current one-shot `core/internal/control` into a persistent,
bidirectional connection.

- **Daemon listens.** At session creation the hub opens the listening socket at
  `control.SocketPath(sess.Name)`. It is the fixed point (long-lived, owns the
  session and queue).
- **Bridge dials + redials.** On startup the bridge connects; on connection loss
  it reconnects using the **existing 3s supervisor backoff**.
- **Both directions on one connection:** daemon → bridge pushes inputs
  (JSON-lines); bridge → daemon returns turn events (the Phase 2 `control.Event`
  protocol, extended to carry an input frame in the other direction).
- **Rupture:** if the bridge dies mid-turn, that turn is lost (the process had
  crashed anyway). On reconnect the FIFO resumes at the next input — no replay of
  the aborted turn.
- The supervisor passes `--control-socket <path>` to the bridge (`bridgeArgs`
  does not do this yet — add it).

## TUI lifecycle: in-process, TTY-gated

TUI runs in-process, conditioned on a TTY.

- `serve` detects an interactive terminal (TTY on stdin/stdout).
- **Foreground (TTY):** the Bubbletea TUI replaces the current `<-ctx.Done()`
  block (`core/host/serve.go:189`); the terminal gateway registers into the hub.
  Quitting the TUI (`q` / Ctrl-C) cancels the context → clean daemon shutdown.
- **Background service (`dctl service`, no TTY):** no TUI, the terminal gateway
  does not register, only Discord runs. Current behavior **strictly preserved**.
- Single binary, single process. Remote `attach` is a future evolution.

## File structure

- `core/internal/control/` — persistent bidirectional connection: Listen
  (daemon) + Dial/redial (bridge); input frames alongside the existing event
  frames.
- `core/host/` — the hub: per-session FIFO queue, input pump (queue → bridge),
  event fan-out (bridge → bound gateways). Stays agnostic (no plugin import).
- `core/bridge/bridge.go` — `runner` reduced to a pure runner: reads input from
  the socket, no direct gateway Post/Reply; emits events (already done in
  Phase 2).
- **terminal gateway plugin** (new) — self-registers via `init()`, implements
  `contracts.Gateway` / `ChannelReader`; fed by the TUI.
- **Bubbletea TUI** (new) — view + input; first external dependency
  (charmbracelet/bubbletea + lipgloss + bubbles).
- `supervisor.go` — `bridgeArgs` adds `--control-socket`.
- `serve.go` / `core/host/serve.go` — TTY detection + TUI loop.

## Testing

- `TestCorePurity` / `TestHostPurity` stay green (terminal gateway is a plugin,
  not in core).
- Unit tests: FIFO queue (ordering, fan-out to all bound gateways), socket
  reconnection (mid-turn loss → resume at next input), pure runner (socket input
  → events), TTY detection (foreground TUI vs background no-TUI).
- Discord re-attached in the second sub-phase, verified by the existing
  integration tests (behavior must be unchanged from the operator's view).

## Out of scope (future)

- Turn interruption / preempt (Ctrl-C of a running turn).
- Remote `attach` (TUI as a separate client process connecting to a background
  daemon).
- Replay of an aborted turn after a bridge crash.
