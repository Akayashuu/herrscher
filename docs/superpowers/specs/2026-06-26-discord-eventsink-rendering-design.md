# Discord gateway owns its rendering via EventSink

**Date:** 2026-06-26
**Status:** Approved
**Repos touched:** `herrscher-discord-gateway` (gateway), `herrscher` (host)

## Problem

The host's `core/host/progress.go` + the rich path of `core/host/renderer.go`
(`gatewayRenderer`) bake Discord-specific presentation into the supposedly
generic host: Unicode emojis per tool, the 2000-char message limit, the 1500 ms
edit throttle, the 15-line cap, and the live-updating progress message.

These leaked into the host only because the Discord gateway does **not**
implement `contracts.EventSink`, so it free-rides on the host's fallback
renderer. The contract already provides the correct seam: a gateway that
implements `EventSink` receives the raw turn-event stream and renders itself
(the terminal gateway already does this, `plugins/terminal/terminal.go:69`).

## Goal

- The Discord gateway implements `EventSink` and renders its live progress view,
  final reply, summary, and emojis itself (via its existing DCTL client).
- Acknowledge receipt of a user turn with a ⏳ reaction, removed at turn end.
- The host becomes genuinely generic: the rich fallback renderer is deleted; a
  non-`EventSink` gateway only gets the final reply posted through `Gateway.Post`.

## Confirmed constraints

- `contracts.EventSink` is `Emit(Event)`. `Event{T, Who, Text, Value, Done, Cost}`
  carries **no channel and no message ID**.
- The Discord gateway is a **shared singleton** across sessions
  (`boundGateways`, `core/host/serve.go:309`), and the whole model is
  **mono-channel** (DCTL = one bot + one `DefaultChannel`). The current renderer
  already posts to `DefaultChannel` (`turnloop.go:128`, `gatewayChannel`), not a
  per-session channel — so rendering to `DefaultChannel` from the gateway is
  behaviour-preserving, not a regression.
- The gateway already satisfies `Gateway` (`Post`, `React`) and `ChannelReader`
  (`Read`, `UpsertStatusMessage`) with DCTL wired (`dctl v1.1.0`).
- `Event.Cost` exists in the gateway's contracts version (v0.1.4) — no contract
  bump required.

## Design

### Gateway: implement `EventSink`

Port the rendering currently in `core/host/progress.go` and the rich path of
`core/host/renderer.go` into the gateway. `Emit(Event)` maps the stream exactly
as `gatewayRenderer.handle` does today:

| Event       | Action                                                                 |
|-------------|------------------------------------------------------------------------|
| `human`     | open a `progressView`; **ACK**: add ⏳ on the last user message         |
| `status`    | tool line — split `"Tool detail"`, icon via `emojiFor`                  |
| `chunk`     | `💭 ` prose line (only when progress level is `full`)                   |
| `reset`     | `pv.finish(true)` (collapse to ⚠️ summary, discard partial turn)        |
| `reply`+Done| post the reply (chunked at 2000) via `Post`; `pv.finish(false)`; remove ⏳ |

- `progressView` lives in the gateway as a single field guarded by a mutex.
  Mono-channel ⇒ one in-flight turn at a time. Live edits go through
  `UpsertStatusMessage`; final reply through `Post`.
- Discord-specific constants move here, where they belong: `maxLines = 15`,
  `progressInterval = 1500ms`, `gatewayMaxLen = 2000`.
- Emojis stay Unicode (📖 ✏️ 🔎 🤖 🌐 📝 🔧, 💭, ⏳, ✅, ⚠️) — they render natively
  on Discord. `emojiFor` moves into the gateway.
- Progress level defaults to `full` (matches the host hardcoding `"full"` at
  `turnloop.go:236` today); configurable via gateway config if cheap.

### ACK without a contract change

`Event` carries no message ID, so the gateway recovers it locally: `Read`
records the last user `m.ID` it handed to the host; `Emit(human)` reacts ⏳ on
that ID and `finish` removes it. Both `Read` and `Emit` are on the same gateway
instance, and the model is mono-channel, so this is safe.

### Host: become generic

- Delete `core/host/progress.go` and the rich `gatewayRenderer` in
  `core/host/renderer.go`.
- `fanOut` (`core/host/turnloop.go`) keeps the branch: a gateway implementing
  `EventSink` gets `Emit(e)`; otherwise the host posts **only** the final reply
  (`reply`+Done) via `Gateway.Post`, chunked. No emojis, no Discord constants in
  the host. The chunk helper may stay if still used by the minimal path.
- Update affected host tests (`core/host/renderer_test.go`,
  `core/host/turnloop_test.go`).

## Execution order (safe)

1. Gateway implements `EventSink` (host keeps working via the fallback the whole
   time).
2. Verify Discord rendering is identical end to end.
3. Remove the host's rich renderer and adjust host tests.

At no point does Discord lose its rendering.

## Out of scope

- Concurrent multi-channel rendering. The whole codebase assumes mono-channel;
  true multi-channel would require `Event` (or the sink) to carry a channel and
  is a separate contract evolution.
