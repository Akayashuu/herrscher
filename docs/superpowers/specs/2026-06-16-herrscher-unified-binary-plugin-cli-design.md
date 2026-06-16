# Herrscher — Unified Binary Consolidation — Design

**Status:** design locked (2026-06-16). Scope trimmed to structural consolidation;
the command-API redesign is deferred behind the Memory module (see §5).

**Goal:** Collapse the Herrscher tooling into a single `herrscher` binary by folding
`herrscherd` (host), `herrscher-cli` (management), and `herrscher-core` (engine
packages) into the umbrella repo. No behaviour changes, no `contracts` changes, no
Discord changes — purely "three repos become one binary."

**Architecture (one line):** the umbrella repo `herrscher` stops being a passive
docs/symlink skeleton and becomes the composition-root that *is* the binary — it
requires `contracts` + the plugins + `dctl` and builds `herrscher`.

---

## 1. Motivation

- Today the platform ships as **three buildable modules** for what, in solo use, is
  one tool: `herrscherd` (the daemon + the legacy dctl CLI verbs), `herrscher-cli`
  (the `plugin`/`update`/`install` management facade), and `herrscher-core` (the
  bridge/host/service/manager packages). Two separate `main()` binaries
  (`herrscherd` and `herrscher-cli`) is friction with no benefit at this stage.
- The umbrella repo `herrscher` currently holds only docs and (broken) symlinks. It
  should be the real composition-root you install:
  `go install github.com/Herrscherd/herrscher@latest`.

Known issues deliberately **left untouched** this round (they belong to the
deferred command-API work in §5): the `herrscherd` help still prints
`dctl — minimal Discord bot CLI`; `buildGateway` hardcodes "first registered
gateway wins"; the legacy dctl verbs live in the host instead of the plugin.

## 2. Module topology (LOCKED)

| Module (repo) | Role |
|---|---|
| `herrscher` (umbrella → binary) | composition-root: `main` + `plugins.go` + daemon wiring (`serve`/`bridge`/`service`) + the legacy CLI verbs (unchanged) + `manage/` (was herrscher-cli) + `core/` packages (was herrscher-core). `go build` → binary `herrscher`. |
| `herrscher-contracts` | authority/regulator — **unchanged this round.** |
| `herrscher-discord-gateway` | gateway plugin — **unchanged this round.** |
| `herrscher-claude-backend` | backend plugin — **unchanged this round.** |
| `dctl` | pure Discord REST library — unchanged. |
| ~~`herrscherd`~~ | absorbed into `herrscher` → **repo deleted**. |
| ~~`herrscher-cli`~~ | absorbed into `herrscher` → **repo deleted**. |
| ~~`herrscher-core`~~ | folded into `herrscher` as `core/` packages → **repo deleted**. |

**Why core folds in (not a separate module):** a separate Go module is justified by
substitution or multi-consumer reuse. `core` has neither today — its sole consumer
is the composition-root; plugins never import it (arrows go to `contracts`). Its
agnosticism is guaranteed by a **`purity_test`** forbidding concrete imports
(`dctl`, any plugin) in `core/...`, *not* by a module split. Extraction back into a
module later stays trivial because the purity guard keeps it clean.

**Why contracts never folds in:** it is the authority every plugin imports. As a
subpackage of the binary, plugins would `import .../herrscher/contracts` —
inverting the dependency graph (plugin → host). Hard no. It also stays a standalone
module so third-party plugins import it without pulling the host.

## 3. Single-binary `herrscher` structure

```
herrscher/
├── main.go            command dispatch: built-ins + manage cmds + legacy verbs
├── plugins.go         managed blank-import manifest (herrscher:plugins markers)
├── serve.go bridge.go service.go channel.go envfile.go   daemon + legacy verbs (from herrscherd)
├── manage/            plugin add/remove, update, install (from herrscher-cli)
├── core/              folded herrscher-core
│   ├── bridge/ config/ host/ service/
│   ├── internal/      control, forge, health, instanceid, manager, state, supervisor, worktree
│   └── purity_test.go forbids concrete imports (dctl, plugins) in core/...
└── docs/              umbrella README + diagrams + specs
```

Import paths rewrite `github.com/Herrscherd/herrscher-core/...` →
`github.com/Herrscherd/herrscher/core/...`. The two former `main()` entry points
(`herrscherd/main.go` verbs + daemon, `herrscher-cli/main.go` management) merge
into one `herrscher` dispatcher: the legacy daemon/verbs keep their current command
names; the management verbs mount as `plugin`/`update`/`install`.

**Command surface after consolidation (unchanged behaviour):** `send`, `reply`,
`read`, `watch`, `react`, `thread`, `channel`, `bridge`, `serve`, `service`
(from the old host) plus `plugin`, `update`, `install` (from herrscher-cli).

## 4. Migration

- New `herrscher/go.mod` module `github.com/Herrscherd/herrscher`, go 1.23, with
  `require`+local `replace` for `dctl`, `herrscher-contracts`,
  `herrscher-discord-gateway`, `herrscher-claude-backend`.
- Move `herrscher-core/*` under `herrscher/core/*`; rewrite every internal import
  path; add `core/purity_test.go`.
- Move `herrscherd/{main,serve,bridge,service,channel,envfile,plugins}.go` (and
  their tests) into `herrscher/` root `package main`; repoint core imports to
  `herrscher/core/...`.
- Move `herrscher-cli/{lifecycle,manifest}.go` (and tests) into `herrscher/manage`
  as `package manage`; fold its `main()` dispatch (`plugin`/`update`/`install`)
  into `herrscher/main.go`. `install` now builds and installs the `herrscher`
  binary; `resolveHost` defaults to the current module (self-host).
- Delete the umbrella's broken `@herrscher/` symlink scheme.
- `go build ./...` green and the full existing test suite (296 tests) green at the
  end of each task.
- **Repo deletion** (`herrscherd`, `herrscher-cli`, `herrscher-core`, local + on
  GitHub) is destructive and public — performed only as the final step, with the
  user's explicit go-ahead already given for this run.

## 5. Deferred — the command-API redesign (gated on the Memory module)

Recorded as the agreed direction; **NOT implemented now.** It only pays off once the
Memory module exists, and most command features turn out to be bridge-specific, so
locking their shape now would be premature.

- **Core shrinks to session-domain methods.** `core` exposes a method API for its
  own domain (sessions: list/start/stop/bridge/status) and nothing about users,
  channels, "who sent a message", or homes.
- **Commands move to the bridges.** `allow`, `home`, and similar are bridge
  concerns (only the bridge knows its users and topology). Each bridge owns its
  command handlers, which call core's session methods.
- **The neutral slash abstraction leaves `contracts`.** `Command`, `CommandData`,
  `Responder`, `InboundCommand`, `ChannelSource`, `CommandRegistrar`, the `Kind*` —
  these model a *bridge-specific* feature (Discord/Telegram have slash commands;
  Instagram DMs do not). They are not universal, so they belong inside the bridge
  plugin, not in `contracts`/core.
- **One command API.** A command is one declaration {name, params, handler}; "slash
  command" is just one *endpoint* a bridge listens on (CLI is another). Plugins add
  commands to this single API.
- **Engine invariant (recorded).** A future orchestration engine consumes only the
  universal plugin ports; engine-specific needs go through optional `Capabilities`
  + `Degrade`, never a new mandatory plugin method.
- **Orchestrator (recorded).** Mediator/policy-owner over the port graph that core
  consults (selection among N plugins, composition, resilience, cross-cutting). It
  has a **hard dependency on Memory** — the registry must verify a Memory plugin is
  present when the Orchestrator category is active.

## 6. Out of scope (this round)

No `contracts` change. No Discord/plugin change. No command-API work. No core→
methods refactor. No removal of the existing slash routing. Purely the three-repo
fold into one `herrscher` binary plus the deletion of the absorbed repos.
