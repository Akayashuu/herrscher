package manager

import (
	"context"
	"fmt"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// listTimeout bounds a forge listing run inline on the dispatch loop so an
// unreachable host or hung CLI cannot wedge the daemon.
const listTimeout = 30 * time.Second

func (h *Handler) handleWorkspace(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	sub, _ := in.Data.Subcommand()
	switch sub {
	case "list":
		return h.workspaceList()
	case "remotes":
		return h.workspaceRemotes(ctx)
	default:
		return errf("unknown /workspace subcommand")
	}
}

func (h *Handler) workspaceList() contracts.CommandResponse {
	if h.st.WorkspaceRoot() == "" {
		return errf("no workspace set — run /set workspace first")
	}
	names := h.localProjects()
	if len(names) == 0 {
		return contracts.CommandResponse{Content: "No git projects in workspace.", Private: true}
	}
	out := "Projects:\n"
	for _, n := range names {
		out += "• " + n + "\n"
	}
	return contracts.CommandResponse{Content: out, Private: true}
}

func (h *Handler) workspaceRemotes(ctx context.Context) contracts.CommandResponse {
	gh, gl := h.fg.Available()
	if !gh && !gl {
		return errf("no gh/glab found — install one and authenticate")
	}
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	repos, err := h.fg.List(lctx)
	if err != nil {
		return errf("list remotes: %v", err)
	}
	if len(repos) == 0 {
		return contracts.CommandResponse{Content: "No remote repos found.", Private: true}
	}
	out := "Remotes:\n"
	for _, r := range repos {
		out += fmt.Sprintf("• [%s] %s\n", r.Forge, r.FullName)
	}
	return contracts.CommandResponse{Content: out, Private: true}
}
