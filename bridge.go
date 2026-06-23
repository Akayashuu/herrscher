package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	claude "github.com/Herrscherd/herrscher-claude-backend"
	"github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/bridge"
	"github.com/Herrscherd/herrscher/core/host"
)

// runBridge runs a pure backend runner against the daemon hub: it dials the hub
// control socket (--hub-socket), takes one input frame per turn, and streams the
// backend's events back over the same connection — it does no gateway I/O itself.
// The default backend is a persistent streaming Claude session keyed on the
// channel id; --backend (stream|oneshot) and the claude flags below select and
// configure it.
func runBridge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	ch := channelFlag(fs)
	cmdStr := fs.String("cmd", "", "base command (default 'claude' in stream mode; the per-message program in one-shot mode)")
	stream := fs.Bool("stream", true, "legacy: only consulted when --backend is unset; --stream=false selects the one-shot backend")
	model := fs.String("model", "", "model for the persistent claude session (e.g. claude-haiku-4-5-20251001)")
	session := fs.String("session", "", "session name (scopes the orchestrator/attachment dir)")
	project := fs.String("project", "", "project name — the shared memory scope (P1: every agent of this game recalls it)")
	agent := fs.String("agent", "", "agent name — the private memory scope (P1: this agent's learned skills)")
	verbose := fs.Bool("v", false, "log activity to stderr")
	backend := fs.String("backend", "", "responder backend: stream (default) | oneshot")
	hubSocket := fs.String("hub-socket", "", "unix socket of the daemon hub: when set, run as a pure backend runner (no gateway polling)")
	fs.Parse(args)

	// The backend is the model edge: core never knows which model answers. The
	// factory closes over the chosen claude config and is built per resolved
	// channel (claude keys its persistent session on the channel id).
	newBackend := func(channelID string) (contracts.Backend, error) {
		return claude.NewBackend(ctx, claude.Config{
			Kind:    *backend,
			Stream:  *stream,
			Cmd:     *cmdStr,
			Model:   *model,
			Verbose: *verbose,
		})
	}

	log := host.Logger(*verbose).With("component", "bridge", "session", *session)
	mem := buildMemory(ctx, log)
	if mem != nil {
		defer mem.Close()
	}
	orch := buildOrchestrator(ctx, mem, *session, *project, *agent, log)
	if orch != nil {
		defer orch.Close()
	}
	return bridge.Run(ctx, newBackend, orch, bridge.Options{
		Channel:   *ch,
		HubSocket: *hubSocket,
	})
}

// buildMemory instantiates the first registered memory plugin from the registry,
// or returns nil when none is compiled in or its config is unset. Memory is
// optional: a config/instantiation failure disables it (logged) rather than
// blocking the bridge, so a vault is opt-in via its plugin's env (OBSIDIAN_VAULT).
func buildMemory(ctx context.Context, log *slog.Logger) contracts.Memory {
	disabled := func(kind string, err error) contracts.Memory {
		log.Debug("memory disabled", "kind", kind, "err", err)
		return nil
	}
	r := host.NewResolver(remoteCategories(), os.Getenv("HERRSCHER_NATS"))
	r.SetLogger(log)
	mem, err := r.Memory(ctx, contracts.Default.Memories(), os.Getenv)
	if err != nil {
		return disabled("memory", err)
	}
	return mem
}

// buildOrchestrator instantiates the first registered orchestrator plugin over
// mem (the conversation-policy edge), or returns nil when none is compiled in.
// The session name is threaded through the config bag (key "session") since it is
// runtime state, not env config. A config/instantiation failure disables it
// (logged) rather than blocking the bridge.
func buildOrchestrator(ctx context.Context, mem contracts.Memory, session, project, agent string, log *slog.Logger) contracts.Orchestrator {
	disabled := func(kind string, err error) contracts.Orchestrator {
		log.Debug("orchestrator disabled", "kind", kind, "err", err)
		return nil
	}
	for _, p := range contracts.Default.Orchestrators() {
		if p.Orchestrator == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return disabled(p.Manifest.Kind, err)
		}
		if cfg.Settings == nil {
			cfg.Settings = map[string]string{}
		}
		// Runtime state threaded through the config bag: the session scopes the
		// rolling transcript; project/agent scope the shared/private memory (P1).
		cfg.Settings["session"] = session
		if project != "" {
			cfg.Settings["memory.project"] = project
		}
		if agent != "" {
			cfg.Settings["memory.agent"] = agent
		}
		orch, err := p.Orchestrator(ctx, cfg, mem)
		if err != nil {
			return disabled(p.Manifest.Kind, err)
		}
		return orch
	}
	return nil
}
