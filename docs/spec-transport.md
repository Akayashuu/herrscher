# Herrscher — Spec C: Distribute more (remote backend/orchestrator + transport hardening) (staged)

This spec is cut into **stages**. Each stage is **one PR**: self-contained, independently testable,
and leaves the daemon in a working state. The implementing agent follows the workflow in
[`../IMPLEMENTATION.md`](../IMPLEMENTATION.md): implement a stage, run the review loop, merge, move to
the next.

**Why this axis.** Only the `memory` category can run out-of-process today; the rest is hardcoded to
in-process:
- `pluginhost.go:25-26` rejects any category that is not `memory`.
- `core/host/serve.go:228-236` warns and skips unsupported categories in `startRemotePluginHosts`.
- `core/host/resolver.go:29-46` exposes only `Memory()`; backend and orchestrator always resolve
  locally (`bridge.go:73`, `bridge.go:86-120`).
- Transport is loopback-only and unauthenticated: NATS/gRPC use plain `Connect`/`NewServer`, binding
  `127.0.0.1:0` (`pluginhost.go:60,64,67`). No mTLS, so no real multi-machine.

This spec generalizes the remote category mechanism to **backend** and **orchestrator**, then adds the
auth/TLS seam that multi-machine needs. It builds on Spec A (timeouts/retry/metrics already in place
for remote calls) — **land Spec A first**.

**Note on repo boundaries.** The skeleton/proxy codegen lives in `herrscher-transport` (separate
module). For each stage that needs new remote surfaces: implement in `herrscher-transport`, tag a
release, bump `herrscher/go.mod`, and verify from `herrscher`'s tests. A stage is done only when
`herrscher` exercises it end to end.

**Conventions for every stage**
- TDD: write the failing test first.
- No `panic`/`log.Fatal` on fallible I/O; wrap errors with `%w`.
- Keep `core`/host gateway-agnostic and plugin-pure — purity tests stay green. The resolver must not
  import concrete plugins.
- Remote paths must reuse the Spec A timeout/retry/backoff and metrics seams — do not reinvent them.
- CI green: `gofmt -l` (no diff), `go vet ./...`, `go test ./...` (each touched module).

---

## Stage C1 — Generalize the remote category mechanism (no new category yet) — **shipped (#20)**

**Goal:** remove the `memory`-only hardcoding so adding a category is registration, not a special
case — without yet enabling a new one. Pure refactor that keeps behavior identical.

**Scope** (`pluginhost.go`, `core/host/serve.go`, `core/host/resolver.go`)
- Replace the `category == "memory"` checks (`pluginhost.go:25-26`, `serve.go:228-236`) with a
  registry/table of supported remote categories; `memory` is the sole registered entry after this
  stage.
- Refactor the resolver so `Memory()` is one instance of a generic "resolve category remote-or-local"
  helper (announcement watch + dial), ready to be reused — but expose no new public resolve method
  yet.
- No functional change: memory still resolves remote under `HERRSCHER_REMOTE=memory`, everything else
  local.

**Acceptance / tests**
- Existing remote-memory tests (`core/host/resolver_remote_test.go`) stay green unchanged.
- A unit test asserts the supported-category registry contains exactly `memory` and that an
  unsupported category in `HERRSCHER_REMOTE` is rejected/skipped with a logged warning (Spec A logger).
- Purity tests green; no new concrete-plugin import reached core.

---

## Stage C2 — Remote orchestrator

**Goal:** let the orchestrator run out-of-process, since it is a natural unit-of-policy and (unlike the
streaming backend) has a request/response-shaped surface that is straightforward to proxy.

**Scope** (`herrscher-transport` skeleton/proxy for the orchestrator port, `core/host/resolver.go`,
`bridge.go`, `pluginhost.go`)
- Add orchestrator skeleton/proxy generation in `herrscher-transport` (mirroring memory).
- Register `orchestrator` as a supported remote category (C1 registry); add the resolver method using
  the generic helper; route `buildOrchestrator` (`bridge.go:86-120`) through the resolver when
  `HERRSCHER_REMOTE` includes `orchestrator`, else local as today.
- Reuse Spec A timeout/retry and metrics on the remote orchestrator calls.

**Acceptance / tests**
- A fake remote orchestrator served via the transport answers a `Context()`/turn-shaping call over
  gRPC and the bridge uses it (end-to-end test like the memory remote test).
- With `orchestrator` absent from `HERRSCHER_REMOTE`, the local orchestrator is used and no NATS/gRPC
  dial occurs (assert zero dials).
- Remote orchestrator failure is retried within budget (Spec A) and surfaces a clean error, not a
  panic; metrics counters move. Purity green.

---

## Stage C3 — Remote backend (streaming)

**Goal:** the hard one — let the Claude backend run out-of-process. Unlike memory/orchestrator, the
backend **streams** turn events and holds per-turn state, so the proxy must stream, not call-and-return.

**Scope** (`herrscher-transport` streaming skeleton/proxy for the Backend port,
`core/host/resolver.go`, `bridge.go`, `pluginhost.go`)
- Add a **streaming** backend skeleton/proxy in `herrscher-transport`: a server-streaming gRPC surface
  that forwards the backend's event stream, preserving ordering and the `reply{done}` turn boundary.
- Register `backend` as a supported remote category; resolve via the generic helper; route the
  backend construction (`bridge.go:73`) through the resolver when `HERRSCHER_REMOTE` includes
  `backend`.
- Handle stream interruption explicitly: a dropped backend stream mid-turn must surface a `hangup`
  equivalent so the turn loop (`core/host/turnloop.go:175-201`) abandons the in-flight turn cleanly and
  the next queued input proceeds — reusing the existing abandonment path, not a new one.

**Acceptance / tests**
- A fake remote backend streams a multi-event turn ending in `reply{done}` over gRPC; the bridge
  renders the same event sequence a local backend would (ordering + done boundary asserted).
- Killing the remote backend stream mid-turn triggers turn abandonment (no hang, no panic) and the
  next input is processed on reconnect — asserted on the turn loop.
- `backend` absent from `HERRSCHER_REMOTE`: local backend, zero dials. Purity + full suite green.

---

## Stage C4 — mTLS and configurable bind for multi-machine

**Goal:** make remote transport safe off-loopback so categories can run on another host. Today
everything is plain `127.0.0.1`; multi-machine needs auth + encryption.

**Scope** (`pluginhost.go`, `core/host/resolver.go`, `transportcfg.go`, `herrscher-transport`)
- Add a TLS seam to both the gRPC server (`pluginhost.go:64`) and the dial path
  (`resolver.go`/transport): load CA + cert + key from configured paths/env (`HERRSCHER_TLS_*`), and
  require client certs (mTLS) when TLS is enabled.
- Make the listener bind configurable (`pluginhost.go:60`, currently `127.0.0.1:0`) and the NATS URL
  already-configurable path (`HERRSCHER_NATS`) usable across hosts; default stays loopback + no TLS so
  single-host deployments are unchanged.
- Fail closed: if TLS is half-configured (cert without key, etc.) the plugin-host refuses to start with
  a descriptive error rather than silently serving plaintext.

**Acceptance / tests**
- With TLS env set to a generated test CA/cert pair, a remote category (memory, from C1) negotiates
  mTLS end to end; a client without a valid cert is rejected.
- Half-configured TLS fails startup with a clear error (table test over the missing-field
  permutations).
- TLS unset → loopback plaintext exactly as today (existing remote tests green); docs updated to
  describe the multi-machine setup. Purity + full suite green.

---

## Permanently out of scope (this spec)
- A service mesh / external load balancer (the NATS discovery + direct gRPC dial is the model).
- Dynamic rebalancing or multi-instance failover of a category (single remote instance per category).
- Auth beyond mTLS (no token/OIDC layer in this spec).
