package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	claude "github.com/Herrscherd/herrscher-claude-backend"
	"github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/bridge"
)

// runBridge links a channel to a backend: it watches for new human messages and,
// for each, asks the backend for a reply and posts it back as a threaded reply.
// The default backend is a persistent streaming Claude session keyed on the
// channel id; --backend (stream|oneshot) and the claude flags below select and
// configure it.
func runBridge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	ch := channelFlag(fs)
	cmdStr := fs.String("cmd", "", "base command (default 'claude' in stream mode; the per-message program in one-shot mode)")
	stream := fs.Bool("stream", true, "legacy: only consulted when --backend is unset; --stream=false selects the one-shot backend")
	model := fs.String("model", "", "model for the persistent claude session (e.g. claude-haiku-4-5-20251001)")
	ensure := fs.String("ensure", "prospector", "if no channel is set, create/reuse a channel with this name")
	interval := fs.Int("i", 5, "poll interval in seconds")
	state := fs.String("state", "", "file to persist the last-seen message id across restarts")
	participants := fs.String("participants", "", "append-only journal of message authors for /session who")
	session := fs.String("session", "", "session name (scopes the participant journal and attachment dir)")
	after := fs.String("after", "", "seed start id for the first run (state file wins once it exists)")
	verbose := fs.Bool("v", false, "log activity to stderr")
	progress := fs.String("progress", "full", "live activity feedback level: off | actions | full")
	progressKeep := fs.Bool("progress-keep", false, "keep the full progress list instead of collapsing to a one-line summary")
	backend := fs.String("backend", "", "responder backend: stream (default) | oneshot")
	controlSocket := fs.String("control-socket", "", "unix socket the daemon forwards select-menu clicks to (set by the daemon)")
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

	// Registry-driven wiring, like serve: instantiate the gateway plugin's
	// GatewaySet from runtime config rather than hand-wiring Discord. The bridge
	// loop needs the channel reader and the outbound messaging port.
	set, err := buildGateway(ctx)
	if err != nil {
		return err
	}
	mem := buildMemory(ctx)
	if mem != nil {
		defer mem.Close()
	}
	return bridge.Run(ctx, set.Reader, contracts.Degrade(set.Gateway), newBackend, mem, bridge.Options{
		Channel:       *ch,
		Ensure:        *ensure,
		Interval:      *interval,
		State:         *state,
		Participants:  *participants,
		Session:       *session,
		After:         *after,
		Verbose:       *verbose,
		Progress:      *progress,
		ProgressKeep:  *progressKeep,
		ControlSocket: *controlSocket,
	})
}

// buildMemory instantiates the first registered memory plugin from the registry,
// or returns nil when none is compiled in or its config is unset. Memory is
// optional: a config/instantiation failure disables it (logged) rather than
// blocking the bridge, so a vault is opt-in via its plugin's env (OBSIDIAN_VAULT).
func buildMemory(ctx context.Context) contracts.Memory {
	for _, p := range contracts.Default.Memories() {
		if p.Memory == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "memory %q disabled: %v\n", p.Manifest.Kind, err)
			return nil
		}
		mem, err := p.Memory(ctx, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "memory %q disabled: %v\n", p.Manifest.Kind, err)
			return nil
		}
		return mem
	}
	return nil
}

// bridgeOptionsHasParticipants exists so a compile-time test can assert the
// --participants journal is wired into bridge.Options.
var bridgeOptionsHasParticipants = bridge.Options{}.Participants
