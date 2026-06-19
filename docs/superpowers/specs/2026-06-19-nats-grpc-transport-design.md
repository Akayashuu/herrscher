# NATS/gRPC distributed transport — design

**Date:** 2026-06-19
**Status:** design approved, implementation pending

## Problem

Today every Herrscher plugin (gateway, backend, memory, orchestrator) self-registers
into the in-process `contracts.Registry` via `init()` (xcaddy pattern), and the host
resolves each port by calling a factory that returns a Go object implementing the
contract. Everything runs in one process — the umbrella binary.

We want plugins to be able to run as **separate processes** (independent deploy,
process isolation, crash containment) while keeping the umbrella mono-binary fully
functional. This must be a **wiring change, not a contract rewrite**: the `Manifest`
shape, the registry query surface, and every port interface stay exactly as they are.

## Scope

**In scope:** process isolation on a single host (local NATS), architected so that
moving to multiple machines is a config change, not a code change.

**Out of scope (YAGNI):** polyglot plugins, horizontal scaling / multiple instances
of the same plugin, cross-datacenter. These are explicitly deferred — the design must
not *prevent* them, but we build none of the machinery for them now.

## Driver

Isolation-first. We pick the simplest thing that delivers independent-process deploy
on one host, and we keep the abstraction clean enough that multi-machine is a later
config flip. We do not pay for distribution we don't need yet.

## Architecture

```
                 ┌──────────────────────────────────────────┐
                 │            herrscher serve (host)          │
                 │                                            │
                 │   resolver ── local? ──► in-proc factory   │
                 │       │                  (contracts iface) │
                 │       └── remote? ─► gRPC proxy ──┐         │
                 │                  (contracts iface) │        │
                 │   supervisor: spawn + restart      │        │
                 └────────┬───────────────────────────┼───────┘
                          │ spawn                      │ gRPC Call()
                          ▼                            ▼
        ┌─────────────────────────────┐   ┌─────────────────────────────┐
        │   plugin process (memory)    │   │   plugin process (backend)   │
        │  gRPC server skeleton        │   │  gRPC server skeleton        │
        │   └► real factory object     │   │   └► real factory object     │
        │  announces over NATS at boot │   │  announces over NATS at boot │
        └──────────────┬──────────────┘    └──────────────┬──────────────┘
                       │ publish/subscribe                 │
                       ▼                                   ▼
                 ┌──────────────────────────────────────────┐
                 │   NATS  (discovery + async event bus)      │
                 │   plugins.announce · session.<name>.events │
                 └──────────────────────────────────────────┘
```

**Split of responsibilities:**
- **NATS** = discovery (`plugins.announce`) + async event bus (`session.<name>.events`).
- **gRPC** = synchronous port calls (request/response method invocations).

## Components

### a) Resolver (host-side indirection)

A resolver sits where the host currently calls a registry factory. For each plugin,
config decides whether it resolves **local** (the existing in-proc factory) or
**remote** (a gRPC proxy). Either branch returns the **same `contracts` interface** —
the rest of the host, and the plugin itself, never learn which mode is in effect.

Three modes all available, with no plugin-side awareness:
- **all-local** — bit-for-bit today's behaviour (default).
- **all-remote** — every plugin a separate process.
- **hybrid** — per-plugin, some in-proc and some remote.

### b) Proxy/stub pair — new module `herrscher-transport`

- **Client proxy** implements a `contracts` port interface; each method marshals its
  arguments to JSON and issues a gRPC `Call`, then unmarshals the result.
- **Server skeleton** receives the gRPC `Call`, unmarshals, invokes the real factory
  object's method, marshals the result back.
- **Generic gRPC service** — one service for all ports:

  ```proto
  service Plugin {
    rpc Call(MethodEnvelope) returns (ResultEnvelope);
  }
  message MethodEnvelope { string port = 1; string method = 2; bytes json_payload = 3; }
  message ResultEnvelope { bytes json_payload = 1; string error = 2; }
  ```

  `contracts` stays the **sole source of truth** for types — they are encoded as JSON
  in the envelope. No per-type `.proto`, no codegen tracking the contracts surface.

### c) NATS announce protocol

At boot each plugin process connects to NATS and publishes on `plugins.announce`:

```json
{ "manifest": { ...the existing contracts.Manifest verbatim... },
  "grpcAddr": "127.0.0.1:<port>",
  "instanceID": "<uuid>" }
```

The host subscribes and feeds these into a **remote Registry of identical shape** to
`contracts.Registry` — same `Manifest`, same query surface. Code that queries the
registry can't tell a remote entry from a local one.

### d) Events / streaming via NATS pub-sub

Ports that stream (notably `Backend.Respond` emitting `BackendEvent`s) publish events
to NATS on `session.<name>.events` rather than gRPC-streaming them. The host
subscribes per session. This keeps the gRPC service a clean unary request/response and
puts fan-out / buffering on the bus that's built for it.

### e) Failure handling

- Supervisor (reuses the bridge supervisor pattern) restarts a dead child.
- While the peer is down, the proxy returns a clear, typed transport error (tested).
- `contracts.Degrade` already covers absent capabilities, so a missing remote plugin
  degrades gracefully exactly like a missing local one — a memory error disables
  memory for that bridge rather than crashing it.
- **MVP limitation (follow-on):** the resolver runs once per bridge process and the
  proxy is pinned to the address it first dialed. gRPC auto-reconnects only if the
  restarted child comes back at the **same** address — which today it does not, since
  `plugin-host` binds an ephemeral `127.0.0.1:0`. So a child restart is not yet
  auto-recovered within a live bridge. Two clean fixes, deferred to a follow-on:
  (a) bind `plugin-host` to a stable address so gRPC's built-in reconnect delivers
  recovery, or (b) keep the resolver's NATS subscription alive and re-dial on a new
  announcement. Liveness (`contracts.Liveness`) + a NATS-disconnect death signal feed
  whichever path is chosen.

## Security

First cut is **localhost-only** (NATS + all plugins on the same host). The threat
model is deliberately narrow; the multi-machine hardening is a later config flip with
**no contract or plugin code change**.

Remote mode requires a **NATS server reachable at `$HERRSCHER_NATS`** (default
`nats://127.0.0.1:4222`); the host and `plugin-host` do not embed one (the embedded
server is test-only). With no NATS running, `plugin-host` fails to connect and is
restarted on the supervisor's backoff until one appears.

| Surface | First cut (localhost) | Multi-machine (config flip) |
|---------|------------------------|------------------------------|
| gRPC port calls | `127.0.0.1` bind, no TLS; trust = loopback + supervisor-spawned child | mTLS; resolver injects `TransportCredentials` instead of `insecure` |
| NATS bus | loopback socket, no auth | NATS creds (NKEY/JWT) + TLS, scoped by subject |
| Plugin identity | auto-announced `instanceID` trusted (child PID known to supervisor) | signed announce / identity carried by NATS creds |
| NATS subjects | `plugins.announce`, `session.<name>.events` open | per-subject permissions in creds |

Security lives entirely in the resolver (host) and the NATS/gRPC config. The plugin
receives an already-established connection and never knows whether it's plaintext or
encrypted — which is what makes "localhost now, multi-machine later" a config change.

## Incremental migration plan

In-process stays the default throughout; the transport is an opt-in per plugin. The
umbrella mono-binary is never broken.

1. **`herrscher-transport` (new module, no plugin touched)** — generic proxy/stub +
   `Call` service + JSON codec + NATS announce client. Tested in isolation against a
   fake plugin. Nothing in the umbrella imports it yet.
2. **Resolver in the host, remote branch config-gated off** — default resolves
   everything in-proc (today's behaviour, bit for bit). Test: all-local mode produces
   exactly today's object graph.
3. **One category remote, behind a flag — `memory` first** (smallest port, fewest
   methods; the SQLite memory plugin is a perfect bench). `serve` spawns the child, it
   announces, the proxy binds. The other three stay in-proc. Validate
   all-local / hybrid / plugin-knows-nothing on this single case.
4. **Extend category by category** — backend, then gateway, then orchestrator, each
   behind its own config bit. Hybrid mode is a regression test at every step.
5. **Multi-machine flip** — purely config (non-loopback gRPC addresses + creds/TLS).
   No plugin code change. Final validation that the abstraction held.

Every step leaves the umbrella **100% functional in all-local** — if the transport
breaks, flip config back to today's behaviour.

## Testing

- `herrscher-transport` unit tests: proxy↔skeleton round-trip per port via a fake
  contracts object; JSON envelope edge cases (errors, nil, capability negotiation).
- Resolver: all-local mode equals the current object graph (golden test).
- Per-category remote: spawn a real child, announce over an embedded NATS server,
  drive port calls through the proxy, assert parity with in-proc results.
- Failure: kill the child mid-session, assert proxy error → re-announce → recovery.
