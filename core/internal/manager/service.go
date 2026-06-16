package manager

import (
	"context"
	"fmt"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// handleService routes /service restart|update. Both run on the deferred path
// (see Slow): update rebuilds from source, restart only restarts. The restart
// is scheduled out-of-band by the updater, so the daemon can answer the
// interaction before it is replaced.
func (h *Handler) handleService(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	if h.up == nil {
		return errf("service control unavailable")
	}
	sub, _ := in.Data.Subcommand()
	switch sub {
	case "restart":
		if err := h.up.Restart(ctx); err != nil {
			return errf("restart: %v", err)
		}
		return contracts.CommandResponse{Content: "🔄 Restarting the daemon…", Private: true}
	case "update":
		pull := !in.Data.OptBool("no_pull")
		version, err := h.up.Build(ctx, pull)
		if err != nil {
			return errf("update: %v", err)
		}
		if err := h.up.Restart(ctx); err != nil {
			return errf("rebuilt %s but restart failed: %v", version, err)
		}
		v := version
		if v == "" {
			v = "(unknown)"
		}
		return contracts.CommandResponse{Content: fmt.Sprintf("✅ Rebuilt `%s`, restarting the daemon…", v), Private: true}
	default:
		return errf("unknown /service subcommand")
	}
}

func (h *Handler) handleAllow(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	sub, _ := in.Data.Subcommand()
	switch sub {
	case "add":
		id, ok := in.Data.Opt("user")
		if !ok {
			return errf("missing user")
		}
		if err := h.st.AddAllow(id); err != nil {
			return errf("save: %v", err)
		}
		return contracts.CommandResponse{Content: "✅ Added to allowlist.", Private: true}
	case "remove":
		id, ok := in.Data.Opt("user")
		if !ok {
			return errf("missing user")
		}
		if err := h.st.RemoveAllow(id); err != nil {
			return errf("save: %v", err)
		}
		return contracts.CommandResponse{Content: "✅ Removed from allowlist.", Private: true}
	case "list":
		return contracts.CommandResponse{Content: fmt.Sprintf("Allowlist: %v", h.st.Allow), Private: true}
	default:
		return errf("unknown /allow subcommand")
	}
}
