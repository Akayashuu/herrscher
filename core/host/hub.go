package host

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/cli"
	control "github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/metrics"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
)

// hub owns the live session set of a running daemon and implements
// contracts.SessionControl so a gateway can change it at runtime. The startup
// path (RunHub) and the runtime path (Dispatch) both bring a session live
// through goLive/goDead, so a session created via a slash command is wired
// exactly like one loaded at boot. The handler (behind reg) performs the
// resource work and supervisor start/stop; the hub owns only the control-socket
// acceptor + RunSession loop that the boot path used to inline.
type hub struct {
	ctx     context.Context
	st      *state.State
	sup     *supervisor.Supervisor
	gws     []Deps
	partDir string
	reg     *cli.Registry
	metrics *metrics.Registry

	dispatchMu sync.Mutex // serializes operator commands (and their reconcile)
	mu         sync.Mutex
	live       map[string]context.CancelFunc // session name → cancel its RunSession
}

func newHub(ctx context.Context, st *state.State, sup *supervisor.Supervisor, gws []Deps, partDir string, reg *cli.Registry, m *metrics.Registry) *hub {
	return &hub{ctx: ctx, st: st, sup: sup, gws: gws, partDir: partDir, reg: reg, metrics: m, live: map[string]context.CancelFunc{}}
}

// goLive wires a session into the running hub: it opens the control-socket
// acceptor and starts its RunSession loop. It does NOT start the supervisor —
// the boot loop and the create handler each own exactly one sup.Start. goLive is
// idempotent: a session already live is left untouched.
func (h *hub) goLive(sess state.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.live[sess.Name]; ok {
		return
	}
	acc, err := control.Accept(control.SocketPath(sess.Name))
	if err != nil {
		fmt.Fprintf(os.Stderr, "dctl serve: session %q: control socket: %v\n", sess.Name, err)
		return
	}
	sctx, cancel := context.WithCancel(h.ctx)
	bound := boundGateways(h.gws, sess.BoundGateways())
	go RunSession(sctx, sess.Name, sess.ChannelID, bound, acc, state.ParticipantsPath(h.partDir, sess.Name), h.metrics)
	h.live[sess.Name] = cancel
}

// goDead cancels a session's RunSession loop (which closes its acceptor and
// removes the socket). The supervisor was already stopped by the close handler.
func (h *hub) goDead(name string) {
	h.mu.Lock()
	cancel := h.live[name]
	delete(h.live, name)
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// reconcile aligns the live set with persisted state: sessions present in state
// but not yet live are brought up, and live sessions no longer in state are torn
// down. It is idempotent and safe to call after every Dispatch.
func (h *hub) reconcile() {
	persisted := map[string]state.Session{}
	for _, s := range h.st.SnapshotSessions() {
		persisted[s.Name] = s
	}
	h.mu.Lock()
	liveNames := make([]string, 0, len(h.live))
	for n := range h.live {
		liveNames = append(liveNames, n)
	}
	h.mu.Unlock()
	for _, n := range liveNames {
		if _, ok := persisted[n]; !ok {
			h.goDead(n)
		}
	}
	for _, s := range persisted {
		h.goLive(s)
	}
}

// Dispatch runs an operator command and reconciles the live set so a session
// create/close takes effect immediately. It implements contracts.SessionControl.
// Commands are serialized: gateways can deliver interactions concurrently, and
// running create/close (plus the reconcile that follows) one at a time keeps the
// existence checks and the live set consistent.
func (h *hub) Dispatch(ctx context.Context, args []string) (string, error) {
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	out, err := h.reg.Dispatch(ctx, args)
	h.reconcile()
	return out, err
}

// Create starts a session from a typed spec. It maps CreateSession into the
// flags the session-create command declares and runs it through the typed
// registry seam (no argv assembly), then reconciles so the new session is live.
// It implements contracts.SessionControl.
func (h *hub) Create(ctx context.Context, spec contracts.CreateSession) (string, error) {
	args := map[string]string{"name": spec.Name}
	setStr := func(k, v string) {
		if v != "" {
			args[k] = v
		}
	}
	setStr("project", spec.Project)
	setStr("clone", spec.Clone)
	setStr("cmd", spec.Cmd)
	setStr("backend", spec.Backend)
	setStr("agent", spec.Agent)
	setStr("extractor", spec.Extractor)
	setStr("journal", spec.Journal)
	if len(spec.Gateways) > 0 {
		args["gateways"] = strings.Join(spec.Gateways, ",")
	}
	if spec.TerminalOnly {
		args["terminal_only"] = "true"
	}
	if spec.Shared {
		args["shared"] = "true"
	}
	if spec.ConsolidateEvery != 0 {
		args["consolidate_every"] = strconv.Itoa(spec.ConsolidateEvery)
	}
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	out, err := h.reg.Run(ctx, []string{"session", "create"}, contracts.Input{Args: args})
	h.reconcile()
	return out, err
}

// Close tears a session down by name. It maps to the session-close flags and
// runs through the typed registry seam, then reconciles. It implements
// contracts.SessionControl.
func (h *hub) Close(ctx context.Context, name string, force bool) (string, error) {
	args := map[string]string{"name": name}
	if force {
		args["force"] = "true"
	}
	h.dispatchMu.Lock()
	defer h.dispatchMu.Unlock()
	out, err := h.reg.Run(ctx, []string{"session", "close"}, contracts.Input{Args: args})
	h.reconcile()
	return out, err
}

// Sessions returns a snapshot of the hub's sessions. It implements
// contracts.SessionControl.
func (h *hub) Sessions() []contracts.SessionInfo {
	sessions := h.st.SnapshotSessions()
	out := make([]contracts.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, contracts.SessionInfo{
			Name:      s.Name,
			ChannelID: s.ChannelID,
			Type:      s.Type,
			Gateways:  s.BoundGateways(),
		})
	}
	return out
}

var _ contracts.SessionControl = (*hub)(nil)
