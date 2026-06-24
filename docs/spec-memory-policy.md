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
- The **write/consolidate** path exists upstream but is **never activated from the host**. The
  `Learner` (`herrscher-orchestrator/learner.go`) extracts facts/skills and persists them via
  `RecordShared`/`RecordPrivate`, the extractor registry is in place
  (`herrscher-orchestrator/extractor_registry.go`), and `register.go` already builds a `Learner` when
  the host names a registered extractor — but the **bridge never sets** `memory.extractor`,
  `memory.journal`, or `memory.consolidate-every`. So `lookupExtractor("")` returns nil, the plain
  `Curator` is built, and nothing is ever learned beyond the raw session transcript.

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

## Stage B1 — Extractor registry: a pluggable, declarative extractor — **shipped (orchestrator v0.1.3)**

**Goal:** make the `Extractor` selectable by config instead of requiring a source patch, so a `Learner`
can be built without a source change to this open plugin.

**What shipped** (`herrscher-orchestrator`, v0.1.3 — pinned by `herrscher/go.mod`)
- A registry keyed by name (`extractor_registry.go`: `RegisterExtractor` / `lookupExtractor`),
  mirroring how backends/gateways register: a blank import runs an `init()` that registers an
  extractor, and `register.go` looks it up by name at construction time.
- `register.go` resolves `cfg.Get("memory.extractor")`: a registered name builds a `Learner`, an
  empty or **unknown** name **fails open to the plain `Curator`** (no learning) rather than erroring.
  This is deliberate — the host stays unaffected when the closed curation plugin is absent, and there
  is no default extractor to fall back to (see below).
- **No default extractor is shipped.** The extractor — the heuristics that decide *what is worth
  remembering* — is the **closed part of the moat**; this open plugin defines only the `Extractor`
  seam (`learner.go`) and the registry. A concrete extractor is plugged in by blank import elsewhere.
  The interface stays open for a future LLM-backed extractor.

**Design note (deviation from the original draft).** An earlier draft of this stage called for a
deterministic marker-based default extractor and a startup *error* on an unknown name. The shipped
v0.1.3 chose **fail-open-to-`Curator`** instead, keeping this plugin extractor-free so the curation
heuristics live entirely in the closed module. The host-side stages below therefore register a
**fake** extractor in tests to exercise the `Learner` path — exactly as the orchestrator's own
`extractor_registry_test.go` does — rather than relying on a built-in one.

**Acceptance / tests** (in `herrscher-orchestrator`, green at v0.1.3)
- Registering an extractor and naming it via `memory.extractor` builds a `*Learner`
  (`TestRegisterBuildsLearnerWhenExtractorRegistered`).
- No extractor named → plain `*Curator` (`TestRegisterFallsBackToCuratorWithoutExtractor`).
- Unknown extractor name → plain `*Curator`, not an error
  (`TestRegisterIgnoresUnknownExtractorName`).
- Module CI green.

---

## Stage B2 — Thread the journal path and consolidation cadence from the bridge

**Goal:** give the Learner its inputs. The bridge must pass a journal path and a cadence so
`Consolidate()` has something to read and a trigger to run.

**Scope** (`herrscher/bridge.go`, `core/internal/manager`, `core/internal/supervisor`,
`core/internal/state`)
- Add `extractor` / `journal` / `consolidate_every` params to `session create` and persist them on
  `state.Session` (`Extractor` / `Journal` / `ConsolidateEvery`) — the user-facing way to turn learning
  on. `consolidate_every` is validated as a non-negative integer.
- The supervisor propagates `--extractor`/`--journal`/`--consolidate-every` to the child bridge the
  same way it already propagates `--project`/`--agent` (`supervisor.go`) — only when set.
- The bridge threads them into the orchestrator config as `memory.extractor`, `memory.journal`, and
  `memory.consolidate-every` (read by `herrscher-orchestrator/register.go`).
- When `extractor` is unset, behavior is exactly as today (plain `Curator`, no learning) — this is the
  default, so existing deployments are byte-for-byte unaffected.

**Acceptance / tests**
- `session create` with `extractor`/`journal`/`consolidate_every` persists them on the Session; a bad
  `consolidate_every` is rejected (`session_learning_test.go`).
- `buildOrchestrator` puts the three keys into the config bag only when set (`bridge_test.go`); the
  supervisor includes the flags in the child argv only when set (`supervisor_test.go`).
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
