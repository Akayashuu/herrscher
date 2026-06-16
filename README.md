# Herrscher

**Umbrella repo.** This repository holds no code of its own — only symlinks to the
member repos that make up the Herrscher platform, plus this overview. Check out all
of them side by side and you can browse the whole family from one place.

Herrscher is a self-hosted bridge between a chat platform and an AI agent. You run a
daemon; it brings a bot online 24/7, exposes slash commands to create and manage
**sessions**, and for each session it turns your messages into prompts, asks a model,
and posts the answer back — streaming tool activity and cost as it goes. Each session
can run in its own git worktree, so an agent can work on real code in isolation.

It is built as a **polyrepo family** wired with hexagonal architecture: a narrow
contract package in the middle, two interchangeable edges (the channel and the
model), an agnostic domain, and a host that bolts them together.

---

## The members

The umbrella tracks only the **agnostic skeleton** — the parts that know neither a
channel nor a model. They live under the `@herrscher/` scope:

| Symlink | Repo | Role | README |
|---------|------|------|--------|
| `@herrscher/contracts/` | herrscher-contracts | The ports: interfaces + neutral types. Zero deps, zero logic. | [↗](@herrscher/contracts/README.md) |
| `@herrscher/core/` | herrscher-core | The agnostic domain: sessions, channels, worktrees, supervision. | [↗](@herrscher/core/README.md) |
| `@herrscher/herrscherd/` | herrscherd | The composition root + CLI — the only binary, the daemon. | [↗](@herrscher/herrscherd/README.md) |

## The official plugins

The two edges are **not** part of the umbrella — they are interchangeable plugins,
each its own repo, compiled into the host on demand (blank import + rebuild, the
xcaddy pattern). These are the official ones:

| Repo | Edge | Role |
|------|------|------|
| [herrscher-discord-gateway](https://github.com/Herrscherd/herrscher-discord-gateway) | channel | Adapts Discord to the `Gateway` port (via `dctl`). |
| [herrscher-claude-backend](https://github.com/Herrscherd/herrscher-claude-backend) | model | Speaks Claude stream-json behind the `Backend` port. |

`dctl` is **not** part of the family either: it is an external dependency — the
low-level Discord REST/WebSocket client that the gateway consumes.

---

## How they fit together

```
                     ┌──────────────────────────┐
                     │       contracts           │   the ports (zero deps, zero logic)
                     └──────────────────────────┘
                        ▲          ▲          ▲
            implements  │          │ consumes │  implements
        ┌───────────────┘     ┌────┴─────┐    └───────────────┐
        │                     │          │                    │
┌───────────────────┐  ┌──────────────┐ │            ┌────────────────────────┐
│ discord (gateway) │  │     core     │ │            │    claude-backend      │
│ Discord ⇄ ports   │  │  the domain  │ │            │   Claude ⇄ Backend     │
│ (plugin)          │  └──────────────┘ │            │   (plugin)             │
└───────────────────┘         ▲         │            └────────────────────────┘
        ▲                     │         │                    ▲
        └──────────┬──────────┴─────────┴────────────────────┘
                   │
          ┌────────────────────┐
          │     herrscherd      │   the only main(); imports core + the plugins + dctl
          └────────────────────┘
```

**The golden rule:** dependency arrows only ever point *toward* `contracts`. The
core depends on no edge; the edges depend on no core; only the host knows the
concrete types of both. That is what lets you swap Discord for Slack, or Claude for
another model, by editing one wiring file in `herrscherd` — never the domain.

For the full architecture, the CLI, and the exact wiring code, read
**[@herrscher/herrscherd/README.md](@herrscher/herrscherd/README.md)** — it is the
canonical entry point.

---

## Layout & wiring

Each member is its own Go module with its own `go.mod`. During development they are
stitched together with `replace` directives pointing at the sibling directories, so
all the repos must sit side by side under the same parent:

```
dev/
├── herrscher/                 ← you are here (symlinks + this README)
│   └── @herrscher/            ← the agnostic skeleton
│       ├── contracts/   → ../../herrscher-contracts
│       ├── core/        → ../../herrscher-core
│       └── herrscherd/  → ../../herrscherd
├── herrscher-contracts/
├── herrscher-core/
├── herrscherd/
├── herrscher-discord-gateway/ ← plugin (not in the umbrella)
├── herrscher-claude-backend/  ← plugin (not in the umbrella)
└── dctl/                      ← external dependency
```

The symlinks resolve only when the siblings are checked out alongside this repo.

---

## Quick start

```bash
# build the single binary from the herrscherd module (siblings must be alongside)
cd ../herrscherd && go build -o herrscherd .

export DISCORD_BOT_TOKEN=...
./herrscherd serve --health-addr :8787
```

See [@herrscher/herrscherd/README.md](@herrscher/herrscherd/README.md) for every CLI subcommand
(`serve`, `bridge`, `service`, `channel`, …) and the configuration layering.

---

## A note on history

The platform grew out of a Go monolith (`dctl`) that bridged Discord to a local
Claude. Herrscher is that monolith decomposed along its natural seams — channel,
model, domain — so each can evolve, and one day distribute over NATS/gRPC,
independently. The contract shapes (`Manifest`, the in-process registry) are
deliberately chosen to make that later transport change a wiring detail, not a
rewrite.
