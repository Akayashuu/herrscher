package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	control "github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/health"
	"github.com/Herrscherd/herrscher/core/internal/instanceid"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
	"github.com/Herrscherd/herrscher/core/service"
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

	// InstanceID is the explicit per-daemon namespace (-instance flag /
	// DCTL_INSTANCE_ID). Empty falls back to DCTL_OWNER_ID, then legacy mode.
	InstanceID string

	// Declarative config.json defaults. Owner is the per-daemon instance-id
	// fallback (env DCTL_OWNER_ID takes precedence, resolved by the caller).
	// Home, Workspace and Source seed state in-memory only if unset, so a live
	// /set always wins (see state.ApplyDefaults).
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

// RunHub is the always-on multi-gateway daemon: it supervises one pure-runner
// bridge per persisted session, drives each session's turns over a control
// Acceptor (fanning events out to every bound gateway), and serves
// health/liveness. gws are the gateway sets the daemon owns (built from the
// registry by the caller). Command dispatch no longer runs here —
// session/service commands run through the operator CLI (see NewRegistry).
func RunHub(ctx context.Context, gws []Deps, o Options) error {
	h := health.NewHealth(time.Now())

	st, err := state.LoadState(o.StatePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	// Seed declarative defaults from config.json in-memory only; a live update
	// (persisted to state.json) keeps precedence.
	var home *state.HomeRef
	if o.Home != nil {
		home = &state.HomeRef{ID: o.Home.ID, Type: o.Home.Type}
	}
	st.ApplyDefaults(home, o.Workspace, o.Source)

	self, _ := os.Executable()
	partDir := filepath.Dir(o.StatePath) // participants/<name>.log lives beside state.json
	sup := supervisor.NewSupervisor(ctx, self)

	for _, sess := range st.SnapshotSessions() {
		acc, err := control.Accept(control.SocketPath(sess.Name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "dctl serve: session %q: control socket: %v\n", sess.Name, err)
			continue
		}
		bound := boundGateways(gws, sess.BoundGateways())
		go RunSession(ctx, sess.Name, bound, acc, state.ParticipantsPath(partDir, sess.Name))
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

	// Plugin discovery: report the plugins compiled into this binary. Each
	// self-registers into contracts.Default from its init() (xcaddy pattern), so
	// adding a gateway or backend is a blank import + rebuild.
	for _, p := range contracts.Default.Plugins() {
		fmt.Fprintf(os.Stderr, "dctl serve: plugin %s (%s)\n", p.Manifest.Kind, p.Manifest.Category)
	}

	// Liveness uses the first gateway exposing each port (e.g. Discord): ping a
	// Prober for reachability, and maintain the status embed via a Reader.
	if o.HealthAddr != "" {
		go serveHealth(ctx, o.HealthAddr, h)
	}
	if pr := firstProber(gws); pr != nil {
		go pingLoop(ctx, pr, h)
	}
	if o.StatusChannel != "" {
		if cr := firstReader(gws); cr != nil {
			go statusLoop(ctx, cr, o.StatusChannel, st, h, instID)
		}
	}

	fmt.Fprintln(os.Stderr, "dctl serve: hub up; supervising sessions; bot online.")
	<-ctx.Done()
	return ctx.Err()
}

// firstProber returns the first gateway's Prober (nil if none expose one).
func firstProber(gws []Deps) contracts.Prober {
	for _, g := range gws {
		if g.Prober != nil {
			return g.Prober
		}
	}
	return nil
}

// firstReader returns the first gateway's ChannelReader (nil if none expose one).
func firstReader(gws []Deps) contracts.ChannelReader {
	for _, g := range gws {
		if g.Reader != nil {
			return g.Reader
		}
	}
	return nil
}

// boundGateways selects, from all built gateway sets, those whose kind is in the
// session's bound set. A bound kind that wasn't built is skipped (its config was
// absent).
func boundGateways(all []Deps, kinds []string) []Deps {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []Deps
	for _, g := range all {
		if g.Gateway != nil && want[g.Gateway.Manifest().Kind] {
			out = append(out, g)
		}
	}
	return out
}
