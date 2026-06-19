package host

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Herrscherd/herrscher/core/cli"
	"github.com/Herrscherd/herrscher/core/internal/agent"
	"github.com/Herrscherd/herrscher/core/internal/forge"
	"github.com/Herrscherd/herrscher/core/internal/manager"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
	"github.com/Herrscherd/herrscher/core/internal/worktree"
	"github.com/Herrscherd/herrscher/core/service"
)

// buildRegistry constructs the session/service command handler over a given
// state + supervisor and registers its commands into a fresh CLI registry. The
// daemon (RunHub) and the operator CLI (NewRegistry) share this so a session
// created either way is built from identical deps. d.Admin supplies the channel
// admin port; instID namespaces shared git/Discord resources.
func buildRegistry(ctx context.Context, d Deps, o Options, st *state.State, sup *supervisor.Supervisor, instID string) (*cli.Registry, error) {
	partDir := filepath.Dir(o.StatePath)
	wt := worktree.NewWorktreer(ctx, instID)
	fg := forge.New()
	upCfg, _ := service.DefaultConfig()
	up := serviceUpdater{cfg: upCfg, st: st}
	agents := agent.NewStore(filepath.Join(partDir, "agents"))
	hdl := manager.NewHandler(d.Admin, sup, wt, fg, up, agents, st, o.DefaultCmd, partDir)

	reg := &cli.Registry{}
	for _, c := range hdl.Commands() {
		if err := reg.Add(c); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// NewRegistry builds the operator CLI registry: it loads its own state +
// supervisor (the operator invocation is a short-lived process) and registers
// the session/service handler's commands. The returned registry dispatches argv
// (see core/cli).
func NewRegistry(ctx context.Context, d Deps, o Options) (*cli.Registry, error) {
	st, err := state.LoadState(o.StatePath)
	if err != nil {
		return nil, err
	}
	var home *state.HomeRef
	if o.Home != nil {
		home = &state.HomeRef{ID: o.Home.ID, Type: o.Home.Type}
	}
	st.ApplyDefaults(home, o.Workspace, o.Source)

	instID, err := resolveInstanceID(st, o.InstanceID, o.Owner)
	if err != nil {
		return nil, err
	}

	self, _ := os.Executable()
	sup := supervisor.NewSupervisor(ctx, self)
	return buildRegistry(ctx, d, o, st, sup, instID)
}
