package manager

import (
	"context"
	"fmt"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// Handler routes slash-command interactions to actions.
type Handler struct {
	d          discord
	sup        supervisor
	wt         worktrees
	fg         forges
	up         updater
	st         *state.State
	defaultCmd string
	partDir    string // dir holding participants/<name>.log journals
	// cmdPresets are the backend-supplied ready-made /session cmd choices (model ×
	// effort). The host is model-agnostic: a backend fills these in, core never
	// hardcodes model ids.
	cmdPresets []contracts.Choice
}

// NewHandler builds a Handler. defaultCmd is the bridge command used when a
// session is created without an explicit cmd. partDir is the directory under
// which per-session participant journals live (participants/<name>.log).
// cmdPresets are the backend-supplied ready-made cmd choices for autocomplete
// (injected so the host stays model-agnostic).
func NewHandler(d discord, sup supervisor, wt worktrees, fg forges, up updater, st *state.State, defaultCmd string, partDir string, cmdPresets []contracts.Choice) *Handler {
	return &Handler{d: d, sup: sup, wt: wt, fg: fg, up: up, st: st, defaultCmd: defaultCmd, partDir: partDir, cmdPresets: cmdPresets}
}

// PartDir returns the participants journal directory (used by tests/wiring).
func (h *Handler) PartDir() string { return h.partDir }

func deny() contracts.CommandResponse {
	return contracts.CommandResponse{Content: "⛔ Not authorized.", Private: true}
}
func errf(f string, a ...any) contracts.CommandResponse {
	return contracts.CommandResponse{Content: "⚠️ " + fmt.Sprintf(f, a...), Private: true}
}

// Slow reports whether an interaction does network/exec work that can exceed
// Discord's 3s callback deadline, so the caller should defer (ack now, edit the
// reply in when ready): session create/close (channel + git ops, optional clone)
// and workspace remotes (gh/glab over the network).
func (h *Handler) Slow(in contracts.Command) bool {
	switch in.Data.Name {
	case "session":
		sub, _ := in.Data.Subcommand()
		return sub == "create" || sub == "close"
	case "workspace":
		sub, _ := in.Data.Subcommand()
		return sub == "remotes"
	case "service":
		// update rebuilds (git + go build); restart schedules an out-of-band
		// restart. Both ack first, then reply once the work is done/queued.
		return true
	}
	return false
}

// Handle processes one interaction and returns the reply.
func (h *Handler) Handle(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	if !h.st.Allowed(in.Invoker) {
		return deny()
	}
	switch in.Data.Name {
	case "set":
		return h.handleSet(ctx, in)
	case "session":
		return h.handleSession(ctx, in)
	case "workspace":
		return h.handleWorkspace(ctx, in)
	case "service":
		return h.handleService(ctx, in)
	case "allow":
		return h.handleAllow(ctx, in)
	default:
		return errf("unknown command %q", in.Data.Name)
	}
}
