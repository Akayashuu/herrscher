package manager

import (
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// Handler holds the dependencies the session/service/agent commands act on.
// Commands (commands.go) turns its methods into declared contracts.Cmd values
// the CLI dispatches.
type Handler struct {
	d          channelAdmin
	sup        supervisor
	wt         worktrees
	fg         forges
	up         updater
	agents     agentStore
	st         *state.State
	defaultCmd string
	partDir    string // dir holding participants/<name>.log journals
}

// NewHandler builds a Handler. defaultCmd is the bridge command used when a
// session is created without an explicit cmd. partDir is the directory under
// which per-session participant journals live (participants/<name>.log). agents
// owns the durable agent homes used to provision sessions.
func NewHandler(d channelAdmin, sup supervisor, wt worktrees, fg forges, up updater, agents agentStore, st *state.State, defaultCmd, partDir string) *Handler {
	return &Handler{d: d, sup: sup, wt: wt, fg: fg, up: up, agents: agents, st: st, defaultCmd: defaultCmd, partDir: partDir}
}

// PartDir returns the participants journal directory (used by tests/wiring).
func (h *Handler) PartDir() string { return h.partDir }

// Agents returns the durable agent store (used by tests/wiring).
func (h *Handler) Agents() agentStore { return h.agents }
