package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/contracts"
	"github.com/Akayashuu/herrscher/discord"
	"github.com/Akayashuu/herrscher/internal/control"
	"github.com/Akayashuu/herrscher/internal/forge"
	"github.com/Akayashuu/herrscher/internal/gateway"
	"github.com/Akayashuu/herrscher/internal/handler"
	"github.com/Akayashuu/herrscher/internal/health"
	"github.com/Akayashuu/herrscher/internal/instanceid"
	"github.com/Akayashuu/herrscher/internal/service"
	"github.com/Akayashuu/herrscher/internal/state"
	"github.com/Akayashuu/herrscher/internal/supervisor"
	"github.com/Akayashuu/herrscher/internal/worktree"
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

// Options holds the parsed flags for the serve daemon.
type Options struct {
	StatePath     string
	DefaultCmd    string
	DefaultInit   []string
	HealthAddr    string
	StatusChannel string
	// InstanceID is the explicit per-daemon namespace (-instance flag /
	// DCTL_INSTANCE_ID). Empty falls back to DCTL_OWNER_ID, then legacy mode.
	InstanceID string
	// Token is the bot token used for the gateway IDENTIFY (same value the
	// client was built with). Sourced from the caller, not read off the client.
	Token string

	// Declarative config.json defaults. Owner seeds the allowlist on first run
	// (env DCTL_OWNER_ID takes precedence, resolved by the caller). Home,
	// Workspace and Source seed state in-memory only if unset, so a live /set
	// always wins (see state.ApplyDefaults).
	Owner     string
	Home      *state.HomeRef
	Workspace string
	Source    string
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

// handleDeferred acks a slow interaction immediately (type 5), runs the handler
// off the dispatch loop, then edits the deferred reply in. On a defer failure it
// falls back to a direct reply so the user is never left without a response.
// handleComponent routes a select-menu click. A dctl choice menu's custom_id
// encodes the session name; the picked value is forwarded to that session's
// bridge over its control socket (the bridge types it into the pane), then the
// click is acknowledged by collapsing the menu so it can't be used twice.
func handleComponent(ctx context.Context, c *dctl.Client, in dctl.Interaction) {
	sess, ok := discord.ParseChoiceCustomID(in.Data.CustomID)
	if !ok {
		return // not a dctl choice menu — ignore
	}
	var value string
	if len(in.Data.Values) > 0 {
		value = in.Data.Values[0]
	}
	ack := "✅ Picked option " + value + "."
	if err := control.Send(control.SocketPath(sess), value); err != nil {
		fmt.Fprintf(os.Stderr, "choice route to %q: %v\n", sess, err)
		ack = "⚠️ Could not deliver the choice (session not running?)."
	}
	if err := c.AckComponent(ctx, in.ID, in.Token, ack); err != nil {
		fmt.Fprintf(os.Stderr, "ack component: %v\n", err)
	}
}

func handleDeferred(ctx context.Context, c *dctl.Client, hdl *handler.Handler, h *health.Health, st *state.State, appID string, in dctl.Interaction) {
	if err := c.DeferInteraction(ctx, in.ID, in.Token, true); err != nil {
		fmt.Fprintf(os.Stderr, "defer: %v\n", err)
		resp := hdl.Handle(ctx, in)
		if err := c.RespondInteraction(ctx, in.ID, in.Token, resp); err != nil {
			fmt.Fprintf(os.Stderr, "respond: %v\n", err)
		}
		h.SetSessions(len(st.SnapshotSessions()))
		return
	}
	resp := hdl.Handle(ctx, in)
	if err := c.EditInteractionResponse(ctx, appID, in.Token, resp); err != nil {
		fmt.Fprintf(os.Stderr, "edit response: %v\n", err)
	}
	h.SetSessions(len(st.SnapshotSessions()))
}

// registerPlugins wires the in-process plugins into the registry. In Phase 1 this
// is replaced by NATS self-registration with the same Manifest.
func registerPlugins(r *contracts.Registry, c *dctl.Client) {
	r.RegisterGateway(discord.NewGateway(c))
}

// Run is the always-on Gateway daemon (gateway + supervisor + liveness).
func Run(ctx context.Context, c *dctl.Client, o Options) error {
	h := health.NewHealth(time.Now())

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
	st.ApplyDefaults(o.Home, o.Workspace, o.Source)

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
	hdl := handler.NewHandler(c, sup, wt, fg, up, st, o.DefaultCmd, o.DefaultInit, partDir)

	// Plugin registry: Discord is registered as a gateway plugin rather than
	// hard-wired. Phase 1 swaps registerPlugins for NATS self-registration.
	var registry contracts.Registry
	registerPlugins(&registry, c)
	fmt.Fprintf(os.Stderr, "dctl serve: registered %d gateway plugin(s)\n", len(registry.Gateways()))

	if err := discord.RegisterCommands(ctx, c); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}
	// Needed to edit deferred interaction replies (webhook @original).
	appID, err := c.AppID(ctx)
	if err != nil {
		return fmt.Errorf("resolve app id: %w", err)
	}

	if o.HealthAddr != "" {
		go serveHealth(ctx, o.HealthAddr, h)
	}
	go pingLoop(ctx, c, h)
	if o.StatusChannel != "" {
		go statusLoop(ctx, c, st, h, o.StatusChannel, instID)
	}

	fmt.Fprintln(os.Stderr, "dctl serve: commands registered; connecting to gateway…")

	// Reconnect loop: a dropped connection just re-IDENTIFYs (no resume).
	for ctx.Err() == nil {
		gw := gateway.NewGateway(c, o.Token, h)
		errCh := make(chan error, 1)
		go func() { errCh <- gw.Run(ctx) }()
	dispatch:
		for {
			select {
			case in := <-gw.Interactions:
				if in.Type == dctl.InteractionComponent {
					// A select-menu click (e.g. a choice prompt). Route it off the
					// dispatch loop so a slow/unreachable bridge can't stall others.
					go handleComponent(ctx, c, in)
					continue
				}
				if in.Type == dctl.InteractionAutocomplete {
					// Off the dispatch loop: Autocomplete shells out to gh/glab
					// (up to acTimeout), which must not stall other interactions.
					// Answer within Discord's ~3s deadline (handler bounds its work).
					go func(in dctl.Interaction) {
						choices := hdl.Autocomplete(ctx, in)
						if err := c.RespondAutocomplete(ctx, in.ID, in.Token, choices); err != nil {
							fmt.Fprintf(os.Stderr, "autocomplete: %v\n", err)
						}
					}(in)
					continue
				}
				if hdl.Slow(in) {
					// Ack within 3s, then do the slow work and edit the reply in
					// off the dispatch loop so one clone can't stall the daemon.
					go handleDeferred(ctx, c, hdl, h, st, appID, in)
				} else {
					resp := hdl.Handle(ctx, in)
					if err := c.RespondInteraction(ctx, in.ID, in.Token, resp); err != nil {
						fmt.Fprintf(os.Stderr, "respond: %v\n", err)
					}
					h.SetSessions(len(st.SnapshotSessions())) // session count may have changed
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
