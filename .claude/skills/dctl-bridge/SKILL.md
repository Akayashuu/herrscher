---
name: dctl-bridge
description: Use when the user wants to link a persistent Claude (or other) session to a Discord channel so the channel becomes conversational — each human message runs a command and its output is posted back. Covers running, persisting, and supervising the bridge.
---

# dctl bridge — link a session to a channel

## Overview

`dctl bridge` turns a Discord channel into a chat front-end for a command. It polls
the channel; for each **human** message (bot messages are skipped → no loops) it runs
the command with the message text as the last arg + on stdin, then posts the command's
stdout back as a reply (chunked at Discord's 2000-char limit). It does **not** run a
poller inside the backend — the bridge is the long-lived process.

Mono-server: one bot token + default channel. If no channel is set it creates/reuses
one (`--ensure`, default `prospector`).

## Run it

```sh
# Link a single persistent Claude conversation to the default channel:
dctl bridge --cmd 'claude -p --continue' \
  --state ~/.local/state/dctl/bridge.last
```

`--continue` keeps one shared conversation across messages. Drop it for a fresh
session per message.

## Flags

| Flag | Default | Use |
|---|---|---|
| `--cmd '<command>'` | (required) | Command run per human message. |
| `-c CHANNEL` | `DISCORD_CHANNEL_ID` | Channel to bridge. |
| `--ensure NAME` | `prospector` | If no channel set, create/reuse this one. |
| `-i N` | `5` | Poll interval (seconds). |
| `--state FILE` | — | Persist last-seen id. **Authoritative**: a restart resumes exactly here and never replays handled messages. Always pass it. |
| `--after ID` | — | Seeds the start id for the **first run only** (ignored once `--state` exists). |
| `--progress LEVEL` | `full` | Live activity feedback per message: `off`, `actions` (tools only), `full` (tools + reasoning). |
| `--progress-keep` | off | Keep the full progress list instead of collapsing to a one-line summary. |
| `-v` | off | Log activity to stderr. |
| `--backend stream\|tmux\|oneshot` | `stream` | Responder strategy (see below). `--stream` is legacy, consulted only when this is unset. |
| `--tmux-timeout DUR` | `5m` | tmux backend: max wait for a turn to settle. |
| `--tmux-init MSG` | — | tmux backend: priming message typed once after the pane settles, before any human turn. **Repeatable** (order preserved). |
| `--control-socket PATH` | — | tmux backend: unix socket the daemon forwards select-menu clicks to. Set automatically by the supervisor; unset standalone (numeric-reply fallback only). |

Per-message environment passed to the command: `DCTL_MSG`, `DCTL_AUTHOR`,
`DCTL_MESSAGE_ID`, `DCTL_CHANNEL`.

## Backends

The bridge can talk to Claude three ways:

- **`stream`** (**default**) — one persistent `claude -p` **stream-json** process.
  Structured, token-frugal, context stays hot. Permission prompts are not
  interactive (Claude runs pre-approved).
- **`oneshot`** — runs `--cmd` fresh per message (arbitrary non-Claude commands).
- **`tmux`** — drives the **interactive `claude` TUI** inside a tmux session and
  relays its **text** back (no screenshots/ANSI). One persistent `claude` per
  channel (`tmux send-keys` in, `capture-pane` out, diffed and chrome-stripped).
  Launched with `--dangerously-skip-permissions`, so tool-permission prompts
  don't appear; other interactive numbered prompts (model/plan pickers, "how do
  you want to proceed") are detected and rendered as clean numbered options. You
  pick by **replying with a number** (typed into the pane next turn) and, under
  the daemon, via a native **select menu** (see *Interactive choices* below).
  Select with `--backend tmux`. Needs the `tmux` binary on PATH — if it's
  missing the bridge logs a warning and **falls back to the `stream` backend**
  automatically; `dctl service install` also flags a missing tmux. You can `tmux attach -t
  dctl-<channel>` (or `dctl-<DCTL_INSTANCE_ID>-<channel>` when that env var is
  set) to land in the same live session the bridge is driving. Multi-line
  messages are sent as one unit via tmux **bracketed paste** (a buffer loaded
  from stdin, then `paste-buffer -p`), so embedded newlines stay literal instead
  of submitting early; a single Enter then commits the whole message.

From the daemon: `/session create name:foo backend:tmux` creates a tmux-backed
session; the backend is persisted, so a daemon restart respawns it the same way.

**Priming (init prompts).** The tmux backend can type N messages into the pane
once it settles, *before* the first human turn — e.g. "read CLAUDE.md and wait".
Three ways, same effect (all persisted into the session so a restart replays
them): `--tmux-init MSG` (repeatable) on `dctl bridge`; `"initPrompts": [...]` in
`config.json` (daemon default for every new session); and `/session create
init:"first || second"` (`||` separates prompts, overrides the config default).
Priming is **best-effort**: a prompt that errors or times out is logged and the
rest — plus the first human message — still go through. Each priming turn
advances the baseline, so the human's first reply never echoes the priming
output back.

**Interactive choices (select menus).** When a tmux turn ends on a numbered
prompt, the bridge renders the question + options and the human picks one of two
ways:

- **Numeric reply** (every mode): reply `1`/`2`/… and it's typed into the pane
  next turn. The standalone fallback, always available.
- **Select menu** (daemon only): the reply carries a native Discord dropdown.
  Clicking it routes through the daemon's gateway to the bridge over a per-session
  **control socket** (`--control-socket`, set automatically by the supervisor for
  tmux sessions), which types the pick into the pane — serialized with normal
  turns so a click never interleaves keystrokes with a message. A standalone
  bridge has no gateway, so it can't receive clicks; it uses the numeric fallback
  only. A failed menu post degrades to plain text, so a turn is never lost.

### Security (read before exposing tmux)

- **The allowlist is the only gate.** With `--dangerously-skip-permissions`, every
  message from an *allowed* author becomes a command Claude runs unprompted. Always
  deploy the tmux backend with `--allow-state` on a **dedicated control channel** —
  never an open channel.
- **The tmux backend runs `--cmd` through a shell.** `tmux new-session` execs the
  command string via `/bin/sh -c`, so shell metacharacters in `--cmd` are
  interpreted (the `stream`/`oneshot` backends exec an explicit argv with no shell).
  Treat `--cmd` as trusted operator input and don't build it from untrusted text.
- The pane working directory is pinned explicitly (the tmux *server* is a daemon
  whose cwd may differ from the bridge's); a stale namesake session is killed before
  a fresh one starts.

## Feedback while it works

The command is slow (a Claude run takes tens of seconds), so the bridge reacts to
each human message **immediately on pickup** with 👀, then swaps it for ✅ once the
answer is posted (⚠️ on empty/error). The human sees the message was registered
without waiting. Reactions need the bot's **Add Reactions** permission; if missing
they're skipped silently (the reply still posts).

Beyond the reaction, in stream mode the bridge posts a single **live progress
message** per question (created on the first tool call, edited in place). At
`--progress full` (default) it shows each tool the session runs plus its reasoning
text; `--progress actions` shows tools only; `--progress off` disables it. On the
**tmux backend** (the default) only tool actions are available — the TUI has no
structured reasoning feed — so `--progress full` behaves like `actions` there.
When the answer is ready the progress message collapses to a one-line summary
(`✅ 6 actions (Bash×2, Read×3, Edit) · 28s · $0.04`) unless `--progress-keep` is set.
Editing needs the bot's **Manage Messages** / send permission; failures are ignored
so the reply still posts.

**State / no-replay:** the bridge marks a message handled (persists its id) *before*
running the command. This guarantees a restart never replays — at the cost that a
crash mid-command drops that one reply rather than re-answering it. Always run with
`--state`; never bake `--after` into a supervised launcher (it only seeds the first
run anyway).

## Keep it running

- **systemd (user):** template at `contrib/dctl-bridge.service`. Set `DISCORD_BOT_TOKEN`
  in its environment, then `systemctl --user enable --now dctl-bridge`.
- **Quick/detached:** `nohup dctl bridge --cmd '…' --state … >/var/log/dctl.log 2>&1 &`.
- Always pass `--state` for any supervised run so a restart resumes instead of
  re-answering old messages.

## Rules

- **Loop safety is built in:** the bridge ignores bot authors. Don't add a second
  process that replies to bot messages or you'll create an echo loop.
- **One bridge per channel.** Two bridges on the same channel double-answer.
- **Token in env only** (`DISCORD_BOT_TOKEN`) — never a flag, never tracked.
- **`--continue` shares context** across every human in the channel — fine for a solo
  control channel, not for multi-user privacy.

## Common mistakes

- No `--state` → every restart replays the whole channel and re-runs the command on
  old messages.
- Bridging a channel the backend also fans out notifications into → the command sees
  the bot's own notifications as input. Skipped automatically (bot author), but keep a
  dedicated control channel to avoid noise.
