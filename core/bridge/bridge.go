// Package bridge implements the bridge as a pure backend runner: the daemon hub
// owns all gateway I/O and feeds inputs over a control socket, the bridge runs
// the injected backend per turn and emits events back. The loop is
// model-agnostic: it never knows which backend (Claude, …) responds.
package bridge

import (
	"context"
	"fmt"

	"github.com/Herrscherd/herrscher-contracts"
)

// BackendFactory builds the model-edge backend for a resolved channel. It is
// injected so core stays free of any model-specific code: the binary supplies a
// factory closing over its chosen backend (e.g. claude.NewBackend). The channel
// id is passed because a backend may key its session/process on it, and the
// channel can be created inside Run.
type BackendFactory func(channelID string) (contracts.Backend, error)

// Options configures one bridge run (parsed from CLI flags by the binary).
type Options struct {
	Channel       string
	Ensure        string
	Interval      int
	State         string
	After         string
	Participants  string // append-only journal of message authors (empty = disabled)
	Session       string // session name (used to scope participant journals and attachments)
	Verbose       bool
	Progress      string // "off" | "actions" | "full" (default "full")
	ProgressKeep  bool   // keep the full running list instead of collapsing to a summary
	ControlSocket string // unix socket the daemon forwards select-menu clicks to (empty = numeric-reply fallback only)
	// HubSocket selects pure-runner (hub) mode: the bridge dials this socket,
	// reads input/pick frames from the daemon hub, and emits turn events back.
	HubSocket string
}

// Run is the bridge entry point: a pure backend runner. It requires a hub socket
// (the daemon hub owns all gateway I/O) and drives the backend over it.
func Run(ctx context.Context, newBackend BackendFactory, orch contracts.Orchestrator, o Options) error {
	switch o.Progress {
	case "", "off", "actions", "full":
	default:
		return fmt.Errorf("invalid --progress %q (want off|actions|full)", o.Progress)
	}
	if o.HubSocket == "" {
		return fmt.Errorf("bridge requires --hub-socket (pure-runner mode)")
	}
	return runHub(ctx, newBackend, orch, o)
}
