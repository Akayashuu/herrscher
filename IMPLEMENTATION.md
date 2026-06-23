# Implementation Workflow

You are the implementing agent for **Herrscher**. Everything you need is in this repo — do not ask
questions. The design is frozen.

Herrscher is a self-hosted, single-binary bridge between chat platforms (Discord, terminal) and AI
agents, built on a hexagonal architecture: a narrow `herrscher-contracts` core with swappable edges
(Gateway, Backend, Memory, Orchestrator), composed at build time via blank imports. The host and core
import zero platform-specific code — this is enforced by the purity tests.

- **The remaining specs (what to build, in order):**
  - **Spec B — P1 Memory Policy (writing side):** [`docs/spec-memory-policy.md`](docs/spec-memory-policy.md)
  - **Spec C — Distribute more (remote backend/orchestrator + transport):** [`docs/spec-transport.md`](docs/spec-transport.md)

## Status

`master` is healthy: agent provisioning shipped, P1 memory **read** scoping is wired, the
remote-`memory` transport works, the full suite passes, and there are no TODO/FIXME markers.

- **Spec A — Robustness & Observability — done.** Structured `slog` logging, exponential backoff with
  jitter on the restart loops, remote timeouts + bounded retry, and runtime metrics on the health
  surface are all merged.
- **Spec B (4 stages) — remaining.** Turn the dormant `Learner` into a real, scoped consolidation
  loop: extractor registry, journal/cadence threading, run+persist scoped facts/skills, docs.
- **Spec C (4 stages) — remaining.** Generalize the remote-category mechanism, then remote
  orchestrator, remote (streaming) backend, and mTLS for multi-machine.

## Order

Work the specs **in this order — B, then C** — and the stages **within each spec in order**. The
rationale:

1. **Spec B first.** It is the highest product value (agents that actually learn) and is independent
   of the transport work — it builds only on the logging already shipped in Spec A.
2. **Spec C last.** The biggest and riskiest (a streaming remote backend, mTLS); it explicitly builds
   on the Spec A timeout/retry/metrics seams that already exist on `master`.

## The loop

Work **one stage at a time**, in order, across both specs (B1→B4, C1→C4). For each stage:

### 1. Implement the stage
- Open a branch and a PR for that stage only.
- Follow TDD: write the failing test first, then the minimal code to pass.
- Honor the conventions in each spec and in the codebase:
  - Keep `core` and the host **gateway-agnostic** — the purity tests (`TestHostPurity`,
    `TestCorePurity`, root `purity_test.go`) must stay green. No new concrete plugin import into core.
  - No `panic`/`log.Fatal` on fallible I/O; wrap errors with `%w`.
  - Use the standard library `log/slog` for operator logging — no third-party logging dependency.
  - Some Spec B/C stages touch upstream modules (`herrscher-contracts`, `herrscher-orchestrator`,
    `herrscher-transport`): implement upstream, tag a patch release, bump `go.mod`, and verify the
    behavior from `herrscher`'s own tests. The stage is not done until `herrscher` exercises it
    end to end.
- The stage's **Acceptance / tests** section is the definition of done.

### 2. Review — run this prompt **3 times**
After the stage is implemented, run the following review **three times in a row**. Each run is a fresh,
skeptical pass; fix what it legitimately finds, then run it again.

```
Review

- Respect des CI
- Décision d'architecture (hexagonal, pureté gateway-agnostic)
- Performance
- Code quality
- Sécurité
- Bug review
- Supprimer les commentaire inutile
- Mettre à jour les doc avec le content actuelle du projet
```

**Avoid false positives.** Be precise. Do not invent issues to look productive, and do not delete
useful comments (the ones that explain *why*). The CI bar is `gofmt -l` (no diff), `go vet ./...`,
`go test ./...` — both in this repo and in any upstream module a stage touched. Distinguish a real
regression from a pre-existing condition and say which.

### 3. Merge and advance
When a review pass **finds nothing real left to fix**, merge the current stage's PR and move to the
next stage. Repeat until the last stage (C4) is done.

## Definition of done

Each stage is one PR, CI green (here and in any upstream module it touched), with its **Acceptance /
tests** section satisfied — without regressing what ships.

- **Spec B done:** with learning enabled, a session's tagged journal consolidates into shared project
  facts and private agent skills, idempotently and scoped; off by default; documented.
- **Spec C done:** backend and orchestrator can run out-of-process behind the generalized remote
  mechanism, the streaming backend abandons turns cleanly on stream loss, and mTLS makes off-loopback
  multi-machine safe — with loopback/in-process remaining the unchanged default.
