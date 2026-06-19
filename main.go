// Command herrscher is the composition root and CLI for a Herrscher host: it wires
// the registered gateway/backend/orchestrator plugins and the core into one
// binary. It exposes the always-on daemon (serve/bridge/service), the session
// verbs, and the host self-management verbs (init/plugin/update/install). It
// stays gateway-agnostic: it never imports a concrete chat adapter (dctl lives
// in the discord-gateway plugin), driving platforms only through the contracts
// gateway port. Output is deliberately minimal so an LLM driving it spends few
// tokens.
//
// Config (env): the active gateway plugin declares its own required vars (the
// discord gateway needs DISCORD_BOT_TOKEN); the host resolves them generically.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Herrscherd/herrscher/manage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// Auto-load a project-root .env so every command (and every plugin's config
	// resolution) sees its vars without an explicit --env-file. Real environment
	// wins over the file, and the daemon propagates os.Environ() to each bridge
	// subprocess, so one .env floods gateway/backend/memory alike.
	//
	// $HERRSCHER_ENV_FILE overrides the path. We resolve to an ABSOLUTE path and
	// re-export it so bridge subprocesses — which run with cmd.Dir set to a
	// per-session worktree — load the *same* file, not a stray .env that happens
	// to sit in that worktree (which would be an env-injection vector). An
	// explicit $HERRSCHER_ENV_FILE is authoritative, so a load failure there is
	// fatal; the implicit ./.env is best-effort (a missing or unreadable file
	// must not break management verbs that need no secrets).
	envPath, explicit := os.Getenv("HERRSCHER_ENV_FILE"), true
	if envPath == "" {
		envPath, explicit = ".env", false
	}
	if abs, err := filepath.Abs(envPath); err == nil {
		envPath = abs
	}
	os.Setenv("HERRSCHER_ENV_FILE", envPath)
	if err := loadEnvFile(envPath); err != nil {
		if explicit {
			fmt.Fprintln(os.Stderr, "herrscher: "+err.Error())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "herrscher: "+err.Error()+" (continuing)")
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Management verbs need no Discord client; dispatch them first.
	switch cmd {
	case "init":
		os.Exit(manage.InitCmd(args))
	case "plugin":
		os.Exit(manage.PluginCmd(args))
	case "update":
		os.Exit(manage.UpdateCmd(args))
	case "install":
		os.Exit(manage.InstallCmd(args))
	}

	ctx := context.Background()

	// The host stays gateway-agnostic: it never builds a Discord (dctl) client.
	// Every runtime verb drives the registered gateway plugin via the contracts
	// port; raw channel poking lives in the dctl library, not in this binary.
	var err error
	switch cmd {
	case "bridge":
		err = runBridge(ctx, args)
	case "serve":
		err = runServe(ctx, args)
	case "session":
		err = runSession(ctx, args)
	case "service":
		err = runService(ctx, args)
	case "plugin-host":
		err = runPluginHost(ctx, args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "herrscher: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "herrscher: "+err.Error())
		os.Exit(1)
	}
}

// channelFlag registers -c/--channel on fs and returns the bound pointer.
func channelFlag(fs *flag.FlagSet) *string {
	ch := fs.String("channel", "", "channel id (default: DISCORD_CHANNEL_ID)")
	fs.StringVar(ch, "c", "", "channel id (shorthand)")
	return ch
}

func usage() {
	fmt.Fprint(os.Stderr, `herrscher — modular Discord<->Claude harness host

  herrscher bridge --cmd '<command>' [-i 5] [--state FILE]
                                              link the channel to a command:
                                              run it per human message, post its
                                              stdout back (e.g. a Claude session)
  herrscher serve [--health-addr :8787] [--status-channel ID] [--state FILE] [--env-file PATH]
                                              always-on Gateway daemon: bot online
                                              24/7, supervises one
                                              bridge per session; --env-file loads
                                              secrets from a file (used by service)
  herrscher session <create|close|list|who> [--name N] [--project P] [--clone R]
               [--cmd '…'] [--backend stream|oneshot] [--shared] [--force]
                                              manage sessions: create a bridged
                                              channel + worktree + backend, close
                                              one, or list/inspect active ones
  herrscher service <install|uninstall|status|restart|update> [--health-addr ADDR]
               [--env-file PATH] [--source DIR] [--no-pull]
                                              manage the serve daemon: install it
                                              as a boot-started native service
                                              (systemd/launchd/Task Scheduler),
                                              restart it, or update = (git pull +)
                                              rebuild from --source (default cwd)
                                              then restart — run after a merge
  herrscher init [--gateway K] [--backend K] [--memory K] [--orchestrator K]
               [--with MODULE] [--list] [--no-build] [--yes]
                                              compose the plugin stack from scratch
                                              (default: discord+claude+obsidian+
                                              orchestrator), seed .env, then build;
                                              kind "none" drops a category. On a
                                              terminal with no stack flags it runs
                                              an interactive wizard (pick plugins,
                                              enter secrets); --yes forces defaults
  herrscher plugin <list|add|remove> [module]  edit the compiled-in plugin set and rebuild
  herrscher update                            bump every compiled-in plugin and rebuild
  herrscher install [-- ARGS]                 build the host then run its service install

env: DISCORD_BOT_TOKEN (required), DISCORD_CHANNEL_ID (default channel)
     DCTL_OWNER_ID (instance-id fallback), DCTL_STATE_DIR (state dir)
`)
}
