---
name: dctl
description: Use when you need to send, read, or reply on Discord from a session, or manage channels, using the dctl CLI/bot. Mono-server (one bot token + a default channel) so no server/channel questions are needed.
---

# dctl — Discord from a session

## Overview

`dctl` is a token-frugal Discord bot CLI. One global token (`DISCORD_BOT_TOKEN`) +
an optional default channel (`DISCORD_CHANNEL_ID`). Output is one line per message
(`id\tauthor\tcontent`) so it's cheap to read. Mono-server: omit `-c` to hit the
default channel — never ask which server/channel.

Build: `go build -o dctl ./cmd/dctl`. Token must come from the environment, never
a flag or a versioned file.

## Quick reference

| Command | Use |
|---|---|
| `dctl send [-c CH] "<text>"` | Post a message; prints its id. |
| `dctl reply [-c CH] <msg_id> "<text>"` | Inline reply (message_reference); prints reply id. |
| `dctl thread [-c CH] <msg_id> "<name>"` | Open a **real thread** off a message; prints the thread's channel id. Post into it with `send -c <thread_id>`. |
| `dctl read [-c CH] [-n 20] [--after ID]` | Recent messages, oldest→newest, one per line. |
| `dctl watch [-c CH] [-i 10] [--after ID]` | Stream new messages forever (foreground). |
| `dctl channel list [--guild ID]` | List channels (`id type name`; type 0=text, 15=forum). |
| `dctl channel create [--forum] <name>` | Create a text (or `--forum`) channel.¹ |
| `dctl channel post <forum_id> <title> <content>` | Open a post (thread) in a forum.¹ |
| `dctl channel ensure <name>` | Find-or-create text channel by name (no duplicate).¹ |
| `dctl channel delete <id>` | **Delete** a channel (irreversible).¹ |

¹ Needs the bot's **Manage Channels** permission. The minimal invite perms
(`68608`) lack it → these return `discord 403 Missing Permissions`. Re-invite
with Manage Channels (perms `68624`) to use channel create/delete/forum.
`send`/`read`/`reply`/`thread` work on the minimal perms.

`-c`/`--channel` overrides the default channel. `--guild` defaults to the bot's
sole server.

## Daemon (`dctl serve`) — always-on bot + slash commands

`dctl serve` is a long-running daemon that holds a **Gateway (websocket)**
connection, so the bot shows as **online 24/7** and exposes native **slash
commands**. It supervises one bridged process (Claude by default) per "session".
Each bridge keeps a **persistent `claude` stream-json process** (context stays
hot; startup overhead paid once, not per message).

```bash
DCTL_OWNER_ID=<your_user_id> dctl serve [--health-addr :8787] [--status-channel <id>]
```

**Run at boot (cross-OS):** `dctl service install` registers `dctl serve` as a
native boot-started service — systemd **user** unit (Linux), launchd
**LaunchAgent** (macOS), or **Task Scheduler** onlogon task (Windows) — then
enables it at boot — and starts it immediately **if a token is already set**.
Secrets never touch the generated unit: the daemon loads an env file
(`~/.config/dctl/dctl.env`, mode 0600) itself via `serve --env-file` (no shell
sourcing), and install creates that file as an empty template only if missing
(never overwriting an existing one). On a first install (empty
template) it's enabled but **not started** so it can't crash-loop; fill that file
with `DISCORD_BOT_TOKEN` etc., then start it with the command install prints.
`dctl service uninstall` removes it;
`dctl service status` reports liveness. Install from an *installed* binary, not
`go run` (the latter's executable path is a temp file).

- **State** lives in `$DCTL_STATE_DIR/state.json` (default `~/.config/dctl/state.json`):
  home, allowlist, sessions, repo. Sessions are respawned on restart.
- **Allowlist**: every slash command is gated by a list of Discord user IDs. Seed
  the owner with `DCTL_OWNER_ID`; manage at runtime with `/allow`.

| Slash command | Effect |
|---|---|
| `/set home <channel>` | Set the **category** or **forum** that holds sessions (type auto-detected). |
| `/session create <name> [cmd] [shared]` | Create a channel (category) or forum post, start a bridge on it. Runs in its **own git worktree** by default (`shared:true` → main checkout). |
| `/session close <name> [force]` | Stop the bridge, remove the worktree (refuses if dirty unless `force:true` — branch `session/<name>` is kept), archive the channel/post. |
| `/session list` | List active sessions. |
| `/allow add\|remove\|list [user]` | Manage the allowlist. |

- **Worktree isolation**: each session gets `<repo>/.dctl-sessions/<name>` on branch
  `session/<name>` (git-ignored). Falls back to shared if not a git repo.
- **Liveness** (depends only on the bot/daemon, never on a Claude session):
  - `--health-addr :8787` → `GET /health` returns `200`+JSON when online, `503` when
    the gateway is down (for UptimeKuma/curl).
  - `--status-channel <id>` → a self-updating embed: `🟢 dctl online · uptime … · ping …ms · N sessions`.
- Needs **Manage Channels** (creates/archives channels) and the bot must have the
  `applications.commands` scope for slash commands to register.

## Rules

- **Read before you reply.** `dctl read` to get the `message_id` and context, then
  `dctl reply <id>`. Replying blind loses the thread.
- **Real thread vs inline reply.** `reply` = a one-off inline reply. `thread` = a
  proper sidebar thread for an ongoing sub-conversation; then `send -c <thread_id>`.
- **Default channel implicit.** Omit `-c` unless the user names another channel.
- **No default channel set?** `dctl channel ensure prospector` creates one without
  duplicating an existing same-name channel.
- **Deletion is destructive.** Never `dctl channel delete` without an explicit user
  request naming the channel.
- **Token stays in env.** `DISCORD_BOT_TOKEN` lives only in the shell/`.env`, never
  in a tracked file or command you echo back.

## Common mistakes

- Posting a notification the prospector backend already fans out (reply/bounce/
  reminder/rdv) → duplicate. The backend posts those automatically.
- Asking which server/channel → it's mono-server; default channel is implicit.
- Passing the token as a flag → it leaks into history/logs. Use the env var.

## Bridging a persistent session to a channel

To make a channel conversational with a long-lived Claude session, use the bridge —
see the `dctl-bridge` skill.
