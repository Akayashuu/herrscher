# Herrscher — Spec B: P1 Memory Policy — the writing side (staged)

This spec is cut into **stages**. Each stage is **one PR**: self-contained, independently testable,
and leaves the daemon in a working state. The implementing agent follows the workflow in
[`../IMPLEMENTATION.md`](../IMPLEMENTATION.md): implement a stage, run the review loop, merge, move to
the next.

**Why this axis.** P1 memory scoping is wired for **reading** but not for **writing/learning**:
- The **read** path is done: `--project` / `--agent` flags (`bridge.go:28-29`) are threaded by the
  supervisor (`core/internal/supervisor/supervisor.go:24-38`) and into the orchestrator config
  (`bridge.go:105-112`), and `herrscher-orchestrator` recalls shared+private into the context
  (`orchestrator.go:59`, `contracts.RecallScoped`). This works today.
- The **write/consolidate** path exists but is **never activated**. The `Learner`
  (`herrscher-orchestrator/learner.go:43-87`) can extract facts/skills and persist them via
  `RecordShared`/`RecordPrivate`, but the bridge never configures `memory.extractor`,
  `memory.journal`, or `memory.consolidate-every`; `extractor_registry.go` is empty. So `Consolidate()`
  is always a no-op and nothing is ever learned beyond the raw session transcript.

The goal of this spec is to make agents actually **learn**: turn the dormant Learner into a real,
configured, scoped consolidation loop.

**Note on repo boundaries.** Some stages touch `herrscher-orchestrator` and `herrscher-contracts`
(separate modules). For each such stage: implement in the upstream module, tag a patch release, bump
the dependency in `herrscher/go.mod`, and verify the wired behavior from `herrscher`'s tests. The
stage is not done until `herrscher` exercises it end to end.

**Conventions for every stage**
- TDD: write the failing test first.
- No `panic`/`log.Fatal` on fallible I/O; wrap errors with `%w`.
- Keep `core`/host gateway-agnostic — purity tests stay green.
- Reuse the existing scope contracts (`herrscher-contracts/memory_scope.go`); do not fork the policy.
- CI green: `gofmt -l` (no diff), `go vet ./...`, `go test ./...` (in each touched module).

---

## Stage B1 — Extractor registry: a pluggable, declarative extractor

**Goal:** make the `Extractor` selectable by config instead of requiring a source patch. Today
`herrscher-orchestrator/extractor_registry.go` is empty and `lookupExtractor()` (`register.go:31`)
finds nothing, so a `Learner` can never be built.

**Scope** (`herrscher-orchestrator`)
- Define a registry keyed by name (mirroring how backends/gateways register), and register at least
  one real default extractor — a deterministic, dependency-free one (e.g. a heuristic/marker-based
  extractor that pulls explicitly-tagged "fact:" / "skill:" lines from the journal). This gives an
  end-to-end learnable path without requiring an LLM call in tests.
- `register.go` resolves `cfg.Get("memory.extractor")` to a registered extractor; an unknown name is a
  clear startup error (not a silent no-op).
- Keep the interface open for a future LLM-backed extractor, but do not implement one here.

**Acceptance / tests**
- Registering and looking up the default extractor by name returns it; an unknown name returns a
  descriptive error.
- The default extractor, given a sample journal containing tagged lines, returns the expected
  shared-fact and private-skill candidates (table test), and ignores untagged noise.
- Module CI green.

---

## Stage B2 — Thread the journal path and consolidation cadence from the bridge

**Goal:** give the Learner its inputs. The bridge must pass a journal path and a cadence so
`Consolidate()` has something to read and a trigger to run.

**Scope** (`herrscher/bridge.go`, `core/internal/supervisor`, `core/internal/state` if a field is
needed)
- Resolve a per-session journal path (e.g. `<worktree>/.neublox/calls.log`, or a configurable
  `--journal` flag defaulting to a documented path) and thread it into the orchestrator config as
  `memory.journal`, alongside `memory.extractor` and `memory.consolidate-every` (read by
  `herrscher-orchestrator/register.go:32`).
- The supervisor propagates any new flag the same way it already propagates `--project`/`--agent`
  (`supervisor.go:24-38`) — only when set, preserving backward compatibility.
- When `memory.extractor` is unset, behavior is exactly as today (plain `Curator`, no learning) — this
  must remain the default so existing deployments are unaffected.

**Acceptance / tests**
- With `memory.extractor` + `memory.journal` set, `buildOrchestrator` constructs a `Learner`-backed
  orchestrator; with them unset it constructs the plain scoped `Curator` (assert the concrete type or
  an observable behavior difference).
- The supervisor includes the new flag in the child bridge args only when the session has it set
  (extends the existing `bridgeArgs` table test).
- Full `herrscher` suite + purity tests green.

---

## Stage B3 — Run consolidation and persist scoped facts/skills

**Goal:** actually invoke `Consolidate()` at the right moment so extracted facts land in **shared**
project memory and extracted skills land in **private** agent memory, honoring the scope.

**Scope** (`herrscher-orchestrator` consolidation trigger + `herrscher` wiring)
- Drive `Consolidate()` from a defined trigger: after a turn completes and/or every
  `memory.consolidate-every` turns (cadence from B2). Ensure it is invoked on the orchestrator the
  bridge already owns — no new long-lived goroutine that can outlive the session without cancellation.
- Confirm the persist path uses `contracts.RecordShared` for facts (scope: `projects/<project>`) and
  `contracts.RecordPrivate` for skills (scope: `agents/<agent>`), as `learner.go:43-87` intends.
- De-dup against already-recorded entries so re-running consolidation is idempotent (no duplicate
  facts every turn) — reuse the contracts' key-dedup, and skip writes when nothing new was extracted.

**Acceptance / tests** (fake Memory port capturing writes)
- A session whose journal gains a tagged fact and a tagged skill, run through the consolidation
  trigger, produces exactly one shared write under `projects/<project>` and one private write under
  `agents/<agent>` — asserted on the fake Memory's recorded scope + key.
- Running consolidation twice over the same journal produces **no** second write (idempotence).
- A session with no extractor configured performs **zero** consolidation writes (default unchanged).

---

## Stage B4 — Document the P1 write model and its edge cases

**Goal:** close the documentation gap the read-side README left open — the write/learning policy and
its sharp edges.

**Scope** (`README.md` memory section, `docs/`)
- Document the full P1 lifecycle: what is recalled (shared project + private agent), what is written
  and when (consolidation cadence), and how to enable it (`memory.extractor`, `memory.journal`,
  `memory.consolidate-every`, or their flags).
- State the policy decisions explicitly: shared memory is multi-writer across an agent fleet; what
  happens on a key collision between shared and private (shared wins, per `memory_scope.go`); that
  consolidation is idempotent; and that learning is **opt-in** (off by default).
- Cross-link from the README so the write model sits next to the existing scope section; ensure no
  README claim outruns what the code does (verify the flags/keys against the resolved config).

**Acceptance / tests**
- The README/docs describe enabling learning with the exact config keys/flags that the code reads
  (verified by grepping the keys against `register.go` / `bridge.go`).
- The collision and idempotence rules are stated and match the contract behavior asserted in B3.
- No documented capability exceeds the implementation.

---

## Permanently out of scope (this spec)
- An LLM-backed extractor (the registry leaves room; not built here).
- Cross-project memory sharing or any memory ACL/attestation model.
- Memory garbage collection / compaction.
