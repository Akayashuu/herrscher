package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/forge"
	"github.com/Herrscherd/herrscher/core/internal/manager"
	"github.com/Herrscherd/herrscher/core/internal/health"
	"github.com/Herrscherd/herrscher/core/internal/instanceid"
	"github.com/Herrscherd/herrscher/core/service"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
	"github.com/Herrscherd/herrscher/core/internal/worktree"
)

// serviceUpdater backs /service update|restart from inside the daemon. It binds
// the service config to the persisted source dir, builds from source, and
// restarts the unit out-of-band so the daemon can reply before it is replaced.
type serviceUpdater struct {
	cfg service.Config
	st  *state.State
}

// buildTimeout bounds the unattended pull+build behind /service update so a
// stuck network fetch or compile can't hold the interaction open until the
// token expires (the CLI path is attended, so it isn't bounded).
const buildTimeout = 5 * time.Minute

func (u serviceUpdater) Build(ctx context.Context, pull bool) (string, error) {
	src := u.st.SourceDir()
	if src == "" {
		return "", fmt.Errorf("no source set — run /set source <path> first")
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	if pull {
		if err := service.Pull(ctx, src); err != nil {
			return "", err
		}
	}
	if err := service.Build(ctx, src, u.cfg.BinPath); err != nil {
		return "", err
	}
	// Refuse to advertise a version we won't restart into: if the new binary
	// can't even run --help, fail here so the handler never schedules a restart.
	if err := service.Smoke(ctx, u.cfg.BinPath); err != nil {
		return "", err
	}
	return service.SourceVersion(ctx, src), nil
}

func (u serviceUpdater) Restart(ctx context.Context) error {
	return service.RestartDetached(ctx, u.cfg)
}

// Deps is the channel the daemon drives. It is the neutral contracts.GatewaySet
// the registry produces, so serve carries no platform-specific imports and works
// with any gateway plugin.
type Deps = contracts.GatewaySet

// Options holds the parsed flags for the serve daemon.
type Options struct {
	StatePath     string
	DefaultCmd    string
	HealthAddr    string
	StatusChannel string

	// CmdPresets are the backend-supplied ready-made /session cmd choices (e.g. a
	// model × effort matrix). Injected so core holds no model-specific knowledge.
	CmdPresets []contracts.Choice
	// InstanceID is the explicit per-daemon namespace (-instance flag /
	// DCTL_INSTANCE_ID). Empty falls back to DCTL_OWNER_ID, then legacy mode.
	InstanceID string

	// Declarative config.json defaults. Owner seeds the allowlist on first run
	// (env DCTL_OWNER_ID takes precedence, resolved by the caller). Home,
	// Workspace and Source seed state in-memory only if unset, so a live /set
	// always wins (see state.ApplyDefaults).
	Owner     string
	Home      *HomeRef
	Workspace string
	Source    string
}

// HomeRef is the seed home channel from config.json: a channel id and its kind
// ("category" | "forum"). Kept platform-neutral so the caller need not import the
// internal state package.
type HomeRef struct {
	ID   string
	Type string
}

// DefaultStatePath returns the default path to the daemon state file.
func DefaultStatePath() string {
	if d := os.Getenv("DCTL_STATE_DIR"); d != "" {
		return filepath.Join(d, "state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dctl", "state.json")
}

// resolveInstanceID computes and freezes the daemon's instanceID, per Spec §2/§8.
//   - An invalid explicit optID is an error.
//   - If the state already carries an id, a different non-empty resolved id is
//     refused (changing it would orphan existing branches/worktrees); a matching
//     or empty resolved id keeps the stored id.
//   - On a fresh state (no id) with a non-empty resolved id and NO sessions, the
//     id is frozen (persisted). If sessions already exist, the daemon stays in
//     legacy (empty) mode so pre-existing sessions are never orphaned.
func resolveInstanceID(st *state.State, optID, ownerID string) (string, error) {
	resolved, err := instanceid.Resolve(optID, ownerID)
	if err != nil {
		return "", err
	}
	if st.InstanceID != "" {
		if resolved != "" && resolved != st.InstanceID {
			return "", fmt.Errorf("instanceID mismatch: state has %q but %q was requested; "+
				"changing it would orphan existing sessions", st.InstanceID, resolved)
		}
		return st.InstanceID, nil
	}
	if resolved == "" {
		return "", nil
	}
	if len(st.SnapshotSessions()) > 0 {
		// Legacy sessions exist; stay non-namespaced so they keep working.
		fmt.Fprintf(os.Stderr, "dctl serve: %d legacy session(s) present; staying in non-namespaced mode\n",
			len(st.SnapshotSessions()))
		return "", nil
	}
	if err := st.SetInstanceID(resolved); err != nil {
		return "", fmt.Errorf("persist instanceID: %w", err)
	}
	return resolved, nil
}

// handleChoicePick routes a menu pick. The adapter has already parsed the menu's
// route into the target session (in.Command.Data.CustomID); the picked value is
// forwarded to that session's bridge over its control socket (the bridge types it
// into the pane), then the pick is acknowledged so the menu can't be reused. The
// neutral Responder owns how the platform collapses/acks the menu.
func handleChoicePick(ctx context.Context, in contracts.InboundCommand) {
	sess := in.Command.Data.CustomID
	if sess == "" {
		return // not a routed choice menu — ignore
	}
	var value string
	if len(in.Command.Data.Values) > 0 {
		value = in.Command.Data.Values[0]
	}
	ack := "✅ Picked option " + value + "."
	if err := control.Send(control.SocketPath(sess), value); err != nil {
		fmt.Fprintf(os.Stderr, "choice route to %q: %v\n", sess, err)
		ack = "⚠️ Could not deliver the choice (session not running?)."
	}
	if err := in.Responder.AckPick(ctx, ack); err != nil {
		fmt.Fprintf(os.Stderr, "ack pick: %v\n", err)
	}
}

// dispatchCommand answers one regular command. It declares the work's neutral
// "slow" hint and hands the handler as the producer; the plugin owns any
// ack-then-edit defer dance behind the slow flag.
func dispatchCommand(ctx context.Context, hdl *manager.Handler, h *health.Health, st *state.State, in contracts.InboundCommand) {
	produce := func(ctx context.Context) contracts.CommandResponse { return hdl.Handle(ctx, in.Command) }
	if err := in.Responder.Respond(ctx, hdl.Slow(in.Command), produce); err != nil {
		fmt.Fprintf(os.Stderr, "respond: %v\n", err)
	}
	h.SetSessions(len(st.SnapshotSessions()))
}

// Run is the always-on Gateway daemon (gateway + supervisor + liveness).
func Run(ctx context.Context, d Deps, o Options) error {
	h := health.NewHealth(time.Now())
	// A command source with a transport keepalive (e.g. the gateway heartbeat
	// ACK) feeds our health so liveness reflects pure connection state. A source
	// without one would leave health permanently offline, so say so loudly.
	if lw, ok := d.Source.(interface{ SetLiveness(contracts.Liveness) }); ok {
		lw.SetLiveness(h)
	} else {
		fmt.Fprintln(os.Stderr, "serve: command source has no liveness keepalive; health will report offline")
	}

	st, err := state.LoadState(o.StatePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	// Seed the allowlist with the owner on first run (env > config, resolved by
	// the caller into o.Owner).
	if o.Owner != "" {
		_ = st.AddAllow(o.Owner)
	}
	// Seed declarative defaults from config.json in-memory only; a live /set
	// (persisted to state.json) keeps precedence.
	var home *state.HomeRef
	if o.Home != nil {
		home = &state.HomeRef{ID: o.Home.ID, Type: o.Home.Type}
	}
	st.ApplyDefaults(home, o.Workspace, o.Source)

	self, _ := os.Executable()
	partDir := filepath.Dir(o.StatePath) // participants/<name>.log lives beside state.json
	sup := supervisor.NewSupervisor(ctx, self)
	sup.PartDir = partDir
	sup.StatePath = o.StatePath // enables per-session allowlist enforcement in bridge children
	// Restart persisted sessions.
	for _, sess := range st.SnapshotSessions() {
		_ = sup.Start(sess)
	}
	h.SetSessions(len(st.SnapshotSessions()))

	instID, err := resolveInstanceID(st, o.InstanceID, o.Owner)
	if err != nil {
		return fmt.Errorf("resolve instance id: %w", err)
	}
	if instID != "" {
		fmt.Fprintf(os.Stderr, "dctl serve: instance %q\n", instID)
	}

	wt := worktree.NewWorktreer(ctx, instID)
	fg := forge.New()
	// service config resolves the daemon's own binary path for /service update;
	// the rare error (no executable/home) leaves an empty BinPath, which Build
	// then reports cleanly rather than blocking daemon startup.
	upCfg, _ := service.DefaultConfig()
	up := serviceUpdater{cfg: upCfg, st: st}
	hdl := manager.NewHandler(d.Admin, sup, wt, fg, up, st, o.DefaultCmd, partDir, o.CmdPresets)

	// Plugin discovery: report the plugins compiled into this binary. Each
	// self-registers into contracts.Default from its init() (xcaddy pattern), so
	// adding a gateway or backend is a blank import + rebuild. Phase 1 swaps the
	// in-process registry for NATS self-registration with the same Manifest.
	for _, p := range contracts.Default.Plugins() {
		fmt.Fprintf(os.Stderr, "dctl serve: plugin %s (%s)\n", p.Manifest.Kind, p.Manifest.Category)
	}

	if err := d.Registrar.Register(ctx); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}

	if o.HealthAddr != "" {
		go serveHealth(ctx, o.HealthAddr, h)
	}
	go pingLoop(ctx, d.Prober, h)
	if o.StatusChannel != "" && d.Reader != nil {
		go statusLoop(ctx, d.Reader, o.StatusChannel, st, h, instID)
	}

	fmt.Fprintln(os.Stderr, "dctl serve: commands registered; connecting to gateway…")

	// Reconnect loop: a dropped connection just re-IDENTIFYs (no resume).
	for ctx.Err() == nil {
		errCh := make(chan error, 1)
		go func() { errCh <- d.Source.Run(ctx) }()
	dispatch:
		for {
			select {
			case in := <-d.Source.Commands():
				switch in.Kind {
				case contracts.KindChoicePick:
					// A menu pick (e.g. a choice prompt). Route it off the dispatch
					// loop so a slow/unreachable bridge can't stall others.
					go handleChoicePick(ctx, in)
				case contracts.KindSuggest:
					// Off the dispatch loop: suggestions shell out to gh/glab (up to
					// acTimeout), which must not stall other interactions. The plugin
					// answers within its callback deadline (handler bounds its work).
					go func(in contracts.InboundCommand) {
						choices := hdl.Autocomplete(ctx, in.Command)
						if err := in.Responder.Suggest(ctx, choices); err != nil {
							fmt.Fprintf(os.Stderr, "suggest: %v\n", err)
						}
					}(in)
				default:
					// Regular command. Off the dispatch loop so one clone's slow work
					// can't stall the daemon; the plugin owns any defer dance.
					go dispatchCommand(ctx, hdl, h, st, in)
				}
			case err := <-errCh:
				fmt.Fprintf(os.Stderr, "gateway closed (%v); reconnecting in 3s…\n", err)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(3 * time.Second):
				}
				break dispatch
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return ctx.Err()
}
