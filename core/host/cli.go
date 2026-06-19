package host

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Herrscherd/herrscher/core/cli"
	"github.com/Herrscherd/herrscher/core/internal/forge"
	"github.com/Herrscherd/herrscher/core/internal/manager"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
	"github.com/Herrscherd/herrscher/core/internal/worktree"
	"github.com/Herrscherd/herrscher/core/service"
)

// NewRegistry builds the operator CLI registry: it constructs the session/service
// handler from the same deps the daemon uses and registers its commands. The
// returned registry dispatches argv (see core/cli). d.Admin supplies the channel
// admin port the session commands need; the other deps are built locally.
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
	partDir := filepath.Dir(o.StatePath)
	sup := supervisor.NewSupervisor(ctx, self)
	wt := worktree.NewWorktreer(ctx, instID)
	fg := forge.New()
	upCfg, _ := service.DefaultConfig()
	up := serviceUpdater{cfg: upCfg, st: st}
	hdl := manager.NewHandler(d.Admin, sup, wt, fg, up, st, o.DefaultCmd, partDir)

	reg := &cli.Registry{}
	for _, c := range hdl.Commands() {
		if err := reg.Add(c); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
