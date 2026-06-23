# Herrscher — Spec A: Robustness & Observability (staged)

This spec is cut into **stages**. Each stage is **one PR**: self-contained, independently testable,
and leaves the daemon in a working state. The implementing agent follows the workflow in
[`../IMPLEMENTATION.md`](../IMPLEMENTATION.md): implement a stage, run the review loop, merge, move to
the next.

**Why this axis.** Today the daemon's failure handling and visibility are thin:
- No retry on remote failures: `core/host/resolver.go:57` and `pluginhost.go:67` fail immediately, and
  a failed dial terminates the bridge (`core/bridge/hub.go:22-25`).
- Naive restart: the supervisor (`core/internal/supervisor/supervisor.go:85`) and the remote
  plugin-host loop (`core/host/serve.go:249`) both sleep a fixed 3s — no exponential backoff, jitter,
  or circuit breaker.
- No explicit timeout on remote gRPC calls (only a 10s deadline on NATS discovery).
- Logging is `fmt.Fprintf(os.Stderr, ...)` only; no structured logging, no levels, no fields.
- No metrics, no tracing, no request IDs threaded through turns.

**Conventions for every stage**
- TDD: write the failing test first.
- No `panic`/`log.Fatal` on fallible I/O; return wrapped errors (`fmt.Errorf("...: %w", err)`).
- Keep `core` and the host **gateway-agnostic** — the purity tests (`TestHostPurity`,
  `TestCorePurity`, root `purity_test.go`) must stay green. No new concrete plugin imports into core.
- Use the standard library `log/slog` for structured logging — do not add a third-party logging dep.
- CI must be green: `gofmt -l` (no diff), `go vet ./...`, `go test ./...`.

---

## Stage A1 — Structured logging with `log/slog`

**Goal:** replace ad-hoc `fmt.Fprintf(os.Stderr, ...)` diagnostics with a single structured logger so
every line carries level + stable fields (session id, category, component), and verbosity is
controllable.

**Scope** (`core/...`, root composition, `serve.go`, `bridge.go`)
- Introduce one logging seam: a package-level `*slog.Logger` constructor (e.g. `core/internal/obs`)
  that builds a text handler to stderr, level driven by the existing verbose flag (`-v`) and/or
  `HERRSCHER_LOG` env (`debug|info|warn|error`, default `info`).
- Thread a logger (or a child logger with `slog.With("session", id)`) into the supervisor, the
  remote plugin-host loop, the turn loop, and bridge startup. Replace the current stderr prints with
  `logger.Info/Warn/Error` calls carrying fields, not interpolated strings.
- Do not change user-facing CLI/TUI output (gateway rendering stays as-is) — this is operator logging
  only, on stderr.

**Acceptance / tests**
- A unit test captures the logger output (custom `slog.Handler` or a buffer) and asserts the
  supervisor restart line is emitted as a structured record with `level=warn` and a `session` field —
  not a raw formatted string.
- `HERRSCHER_LOG=warn` suppresses info records; `HERRSCHER_LOG=debug` includes them (table test over
  levels).
- Purity tests and the full suite stay green; `gofmt`/`go vet` clean.

---

## Stage A2 — Exponential backoff with jitter for restart loops

**Goal:** make crash-restart resilient instead of a fixed 3s sleep, so a tight crash loop does not
hammer the process table and a recovered dependency is retried quickly.

**Scope** (`core/internal/supervisor/supervisor.go`, `core/host/serve.go`)
- Add a small backoff helper (e.g. `core/internal/obs/backoff.go` or `core/internal/supervisor`):
  configurable base, factor, max cap, and full jitter; **reset to base after a sufficiently long
  successful run** (so a process that ran healthily for minutes restarts fast, while a flapping one
  backs off).
- Apply it to both the supervisor's bridge-restart loop (currently `supervisor.go:85`, sleep 3s) and
  the remote plugin-host restart loop (`serve.go:249`).
- Make the backoff deterministic under test by injecting the randomness/jitter source and the sleep
  function (clock seam) — no real sleeps in tests.

**Acceptance / tests**
- A unit test drives the backoff seam: consecutive failures grow the delay geometrically up to the
  cap; a success that lasted past the reset threshold returns the next delay to base.
- Jitter stays within `[base*factor^n * (1-j), base*factor^n]` bounds (asserted with the injected RNG).
- The supervisor still restarts the bridge after a simulated exit (existing supervisor test stays
  green), now logging via the Stage A1 logger.

---

## Stage A3 — Timeouts and bounded retry on remote calls

**Goal:** stop a slow or briefly-unavailable remote dependency from blocking or killing a session.
A transient NATS/gRPC failure should be retried within a deadline, not fatal.

**Scope** (`core/host/resolver.go`, the remote dial path, `pluginhost.go`)
- Put a `context.Context` deadline on the remote dial/call path (NATS connect, announcement wait, and
  the gRPC dial) — surface a typed error on exhaustion rather than blocking unbounded.
- Wrap the remote resolve (`dialRemoteMemory`) in a bounded retry using the Stage A2 backoff: N
  attempts within a total deadline, each attempt logged at `debug`, the final failure at `warn` with
  the category and elapsed time.
- Keep the existing in-process default path untouched and fast — retry/timeout applies only when a
  category is configured remote (`HERRSCHER_REMOTE`).

**Acceptance / tests**
- A fake transport that fails the first K attempts then succeeds: resolve succeeds within the deadline
  and the attempt count matches; logged warn-on-give-up when K exceeds the budget.
- A transport that never answers: resolve returns a deadline error within the configured budget (test
  uses the injected clock — no real wall-clock wait), and the caller degrades/fails cleanly without
  a panic.
- In-process (no `HERRSCHER_REMOTE`) resolution path is unchanged and incurs no retry/timeout
  overhead (asserted: zero calls to the retry seam).

---

## Stage A4 — Runtime metrics surfaced on the health endpoint

**Goal:** give operators counters/latencies without a new metrics stack, reusing the existing health
surface (`core/internal/health/health.go`, status embed at `core/host/serve.go:211-220`).

**Scope** (`core/internal/health`, `core/host`)
- Add a lightweight in-process metrics registry (atomic counters + a small latency summary; stdlib
  only) tracking at minimum: turns started/completed/abandoned, bridge restarts, remote resolve
  attempts/failures, and remote call latency (count + p50/p95 from a bounded histogram or reservoir).
- Expose them through the existing health/status path (the periodic status embed and any health
  query) as structured fields — no new HTTP server, no Prometheus dependency in this stage.
- Wire the increments at the sites instrumented in A1–A3 (turn loop, supervisor, resolver) behind the
  metrics seam so they stay testable.

**Acceptance / tests**
- Driving a fake turn loop through start→complete and start→abandon moves the corresponding counters;
  a simulated bridge restart bumps the restart counter.
- Remote resolve failures (reusing the A3 fake transport) increment the failure counter and record a
  latency sample; the health snapshot reports them.
- The status/health snapshot serializes the metrics deterministically (golden/struct assertion), and
  the suite + purity tests stay green.

---

## Permanently out of scope (this spec)
- Distributed tracing / OpenTelemetry export (revisit only if multi-machine lands — Spec C).
- A Prometheus/StatsD scrape endpoint (the health surface is the contract for now).
- Changing user-facing gateway rendering — operator observability only.
