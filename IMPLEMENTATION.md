# Implementation Workflow

You are the implementing agent for **Herrscher**. Everything you need is in this repo — do not ask
questions. The design is frozen.

Herrscher is a self-hosted, single-binary bridge between chat platforms (Discord, terminal) and AI
agents, built on a hexagonal architecture: a narrow `herrscher-contracts` core with swappable edges
(Gateway, Backend, Memory, Orchestrator), composed at build time via blank imports. The host and core
import zero platform-specific code — this is enforced by the purity tests.

- **The specs (two axes, staged):**
  - **Spec B — P1 Memory Policy (writing side):** [`docs/spec-memory-policy.md`](docs/spec-memory-policy.md)
  - **Spec C — Distribute more (remote backend/orchestrator + transport):** [`docs/spec-transport.md`](docs/spec-transport.md)

## Status

`master` is healthy: agent provisioning shipped, P1 memory **read** scoping is wired, learning is
opt-in-activatable, the remote-`memory` transport works behind a generalized category registry, the
full suite passes, and there are no TODO/FIXME markers.

Per-stage status (a stage marked **done** carries its PR number in the spec header):

| Spec | Stage | Status |
|------|-------|--------|
| A | A1–A4 — Robustness & Observability | ✅ **done** (slog, backoff+jitter, remote timeout+retry, health metrics) |
| B | B1 — extractor registry | ✅ **done** (shipped upstream `orchestrator@v0.1.3`, doc reconciled #18) |
| B | B2 — thread journal/cadence + `session create` activation | ✅ **done** (#19) |
| B | **B3 — prove the write loop end to end; lock scope + idempotence** | ⬜ **TODO — next** |
| B | **B4 — document the P1 write model** | ⬜ **TODO** |
| C | C1 — generalize the remote-category mechanism | ✅ **done** (#20) |
| C | **C2 — remote orchestrator** | ⬜ **TODO** |
| C | **C3 — remote (streaming) backend** | ⬜ **TODO** |
| C | **C4 — mTLS + configurable bind for multi-machine** | ⬜ **TODO** |

**Remaining work = the five ⬜ stages. Build all of them**, in the order below, until none are left.

## Order

Work the open stages in this exact sequence — **B3 → B4 → C2 → C3 → C4** — finishing Spec B before
starting the rest of Spec C. The rationale:

1. **Finish Spec B first (B3, then B4).** Highest product value (agents that actually learn) and nearly
   done — B1/B2 already ship the activation, so B3 is mostly end-to-end proof and B4 is docs. Closing it
   is cheap and unblocks confident use of learning.
2. **Then Spec C (C2 → C3 → C4), in order.** C1 already generalized the mechanism; C2 (remote
   orchestrator) is the straightforward request/response case, C3 (streaming backend) is the hard one,
   and C4 (mTLS) is the cross-cutting hardening that lands last. Each builds on the Spec A
   timeout/retry/metrics seams already on `master`.

Within each spec, never skip ahead: a later stage assumes its predecessors merged.

## The loop

Work **one stage at a time**, in the order above (B3 → B4 → C2 → C3 → C4). Do not stop after one
stage — keep going until every ⬜ stage is merged. For each stage:

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
When a review pass **finds nothing real left to fix**, merge the current stage's PR and immediately
move to the next ⬜ stage in the order **B3 → B4 → C2 → C3 → C4**. Update this file's status table
(flip the stage to ✅ with its PR number, and mark the next one **next**). Repeat until **all five**
remaining stages are merged — the job is not done while any ⬜ remains.

## Definition of done

Each stage is one PR, CI green (here and in any upstream module it touched), with its **Acceptance /
tests** section satisfied — without regressing what ships.

- **Spec B done:** with learning enabled, a session's tagged journal consolidates into shared project
  facts and private agent skills, idempotently and scoped; off by default; documented.
- **Spec C done:** backend and orchestrator can run out-of-process behind the generalized remote
  mechanism, the streaming backend abandons turns cleanly on stream loss, and mTLS makes off-loopback
  multi-machine safe — with loopback/in-process remaining the unchanged default.
