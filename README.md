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

| Symlink | Repo | Role | README |
|---------|------|------|--------|
| `contracts/` | herrscher-contracts | The ports: interfaces + neutral types. Zero deps, zero logic. | [↗](contracts/README.md) |
| `core/` | herrscher-core | The agnostic domain: sessions, channels, worktrees, supervision. | [↗](core/README.md) |
| `claude-backend/` | herrscher-claude-backend | The model edge: speaks Claude stream-json. | [↗](claude-backend/README.md) |
| `discord/` | herrscher-discord-gateway | The channel edge: adapts Discord (via `dctl`). | [↗](discord/README.md) |
| `host/` | herrscher-host | The composition root + CLI — the only binary. | [↗](host/README.md) |

`dctl` is **not** part of the family: it is an external dependency — the low-level
Discord REST/WebSocket client that the gateway consumes.

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
└───────────────────┘  └──────────────┘ │            └────────────────────────┘
        ▲                     ▲          │                    ▲
        └──────────┬──────────┴──────────┴────────────────────┘
                   │
          ┌────────────────────┐
          │        host         │   the only main(); imports all of the above + dctl
          └────────────────────┘
```

**The golden rule:** dependency arrows only ever point *toward* `contracts`. The
core depends on no edge; the edges depend on no core; only the host knows the
concrete types of both. That is what lets you swap Discord for Slack, or Claude for
another model, by editing one wiring file in `host` — never the domain.

For the full architecture, the CLI, and the exact wiring code, read
**[host/README.md](host/README.md)** — it is the canonical entry point.

---

## Layout & wiring

Each member is its own Go module with its own `go.mod`. During development they are
stitched together with `replace` directives pointing at the sibling directories, so
all five repos must sit side by side under the same parent:

```
dev/
├── herrscher/                 ← you are here (symlinks + this README)
├── herrscher-contracts/
├── herrscher-core/
├── herrscher-claude-backend/
├── herrscher-discord-gateway/
├── herrscher-host/
└── dctl/                      ← external dependency
```

The symlinks (`contracts → ../herrscher-contracts`, `core → ../herrscher-core`,
`claude-backend → ../herrscher-claude-backend`, `discord →
../herrscher-discord-gateway`, `host → ../herrscher-host`) resolve only when the
siblings are checked out alongside this repo.

---

## Quick start

```bash
# build the single binary from the host module (siblings must be alongside)
cd ../herrscher-host && go build -o dctl .

export DISCORD_BOT_TOKEN=...
./dctl serve --health-addr :8787
```

See [host/README.md](host/README.md) for every CLI subcommand (`serve`, `bridge`,
`service`, `channel`, …) and the configuration layering.

---

## A note on history

The platform grew out of a Go monolith (`dctl`) that bridged Discord to a local
Claude. Herrscher is that monolith decomposed along its natural seams — channel,
model, domain — so each can evolve, and one day distribute over NATS/gRPC,
independently. The contract shapes (`Manifest`, the in-process registry) are
deliberately chosen to make that later transport change a wiring detail, not a
rewrite.
