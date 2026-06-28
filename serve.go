package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/config"
	"github.com/Herrscherd/herrscher/core/host"
)

// or returns a if non-empty, else b — used to layer config.json defaults under
// env vars when seeding a flag's default value.
func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// --config is read up front (before Parse) so config.json can seed the other
	// flags' defaults; it's still registered for --help and validation.
	fs.String("config", config.DefaultPath(), "path to the declarative config.json (optional)")
	cfg, err := config.Load(scanFlag(args, "config", config.DefaultPath()))
	if err != nil {
		return err
	}

	statePath := fs.String("state", host.DefaultStatePath(), "path to the daemon state file")
	// Flag defaults are layered as: env > config.json > built-in. An explicitly
	// passed flag then wins naturally (Parse overwrites the default).
	defaultCmd := fs.String("cmd", or(cfg.Cmd, "claude"), "default bridged base command for new sessions (stream-json mode adds -p and the stream flags)")
	healthAddr := fs.String("health-addr", cfg.HealthAddr, "if set (e.g. :8787), serve GET /health")
	statusChannel := fs.String("status-channel", cfg.StatusChannel, "if set, maintain a self-updating status embed there")
	instanceID := fs.String("instance", or(os.Getenv("DCTL_INSTANCE_ID"), cfg.Instance), "per-daemon instance id (slug) used to namespace shared Discord/git resources; defaults to DCTL_INSTANCE_ID then config.json")
	envFile := fs.String("env-file", "", "load DISCORD_BOT_TOKEN and other vars from this file before starting (used by `dctl service`)")
	fs.Parse(args)
	if *envFile != "" {
		// Load secrets in Go rather than via a shell/batch wrapper, so the gateway
		// plugin's config resolution below sees them (the service passes its file
		// here; the implicit ./.env was already loaded in main).
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	// Owner: env DCTL_OWNER_ID wins over config.json (the owner id is kept in
	// env alongside the token), then config seeds it for declarative setups.
	owner := or(os.Getenv("DCTL_OWNER_ID"), cfg.Owner)
	var home *host.HomeRef
	if cfg.Home != nil && cfg.Home.ID != "" {
		home = &host.HomeRef{ID: cfg.Home.ID, Type: cfg.Home.Type}
	}

	// Registry-driven wiring: the daemon instantiates every gateway from the
	// plugin registry rather than hand-wiring Discord. Each plugin self-registered
	// into contracts.Default from its init() (blank import in plugins.go); here we
	// build each one's GatewaySet from runtime config. Adding a gateway is a blank
	// import + rebuild — no code change here.
	hub, err := host.BuildHub(ctx, contracts.Default.Gateways(), os.Getenv)
	if err != nil {
		return err
	}
	var gws []host.Deps
	// A gateway may own the process's main thread (a TUI). We detect it on the
	// raw gateway before Degrade wraps it, and the composition root stays
	// gateway-agnostic: it runs whichever bound gateway implements Foreground
	// rather than importing a concrete frontend.
	var fg contracts.Foreground
	for _, kind := range hub.Kinds() {
		if set, ok := hub.Get(kind); ok {
			if f, ok := set.Gateway.(contracts.Foreground); ok && fg == nil {
				fg = f
			}
			set.Gateway = contracts.Degrade(set.Gateway)
			gws = append(gws, set)
		}
	}

	opts := host.Options{
		StatePath:     *statePath,
		DefaultCmd:    *defaultCmd,
		HealthAddr:    *healthAddr,
		StatusChannel: *statusChannel,
		InstanceID:    *instanceID,
		Owner:         owner,
		Home:          home,
		Workspace:     cfg.Workspace,
		Source:        cfg.Source,

		RemoteCategories: remoteCategories(),
		ForegroundBound:  fg != nil,
	}

	// A bound foreground gateway + interactive TTY → run it on the main thread;
	// quitting it cancels ctx and stops the daemon. No foreground gateway, or a
	// background service (no TTY) → headless: the hub drives every gateway and
	// nothing takes over the foreground.
	if fg != nil && term.IsTerminal(int(os.Stdout.Fd())) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = host.RunHub(ctx, gws, opts) }()
		return fg.RunForeground(ctx, cancel)
	}
	return host.RunHub(ctx, gws, opts)
}

// runSession dispatches the operator `session` commands (create/close/list/who)
// through the core CLI registry. It builds the same gateway + handler deps the
// daemon uses from config.json + env, so a session created here matches one the
// daemon supervises.
func runSession(ctx context.Context, args []string) error {
	return runRegistryVerb(ctx, "session", args)
}

func runAgent(ctx context.Context, args []string) error {
	return runRegistryVerb(ctx, "agent", args)
}

// runRegistryVerb builds the operator registry (the same one the daemon serves)
// and dispatches a single top-level verb through it, printing any output. Both
// session and agent verbs share this so the binary and the gateways drive an
// identical command surface.
func runRegistryVerb(ctx context.Context, verb string, args []string) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	deps, err := buildGateway(ctx)
	if err != nil {
		return err
	}
	var home *host.HomeRef
	if cfg.Home != nil && cfg.Home.ID != "" {
		home = &host.HomeRef{ID: cfg.Home.ID, Type: cfg.Home.Type}
	}
	reg, err := host.NewRegistry(ctx, deps, host.Options{
		StatePath:  host.DefaultStatePath(),
		DefaultCmd: or(cfg.Cmd, "claude"),
		InstanceID: or(os.Getenv("DCTL_INSTANCE_ID"), cfg.Instance),
		Owner:      or(os.Getenv("DCTL_OWNER_ID"), cfg.Owner),
		Home:       home,
		Workspace:  cfg.Workspace,
		Source:     cfg.Source,
	})
	if err != nil {
		return err
	}
	out, err := reg.Dispatch(ctx, append([]string{verb}, args...))
	if err != nil {
		return err
	}
	if out != "" {
		fmt.Println(out)
	}
	return nil
}

// buildGateway returns the first registered gateway's GatewaySet, built through
// the multi-gateway hub. Behavior is unchanged from the pre-hub version (first
// gateway wins); the hub additionally tolerates other gateways whose config is
// absent. A new gateway is still just a blank import + rebuild.
func buildGateway(ctx context.Context) (host.Deps, error) {
	hub, err := host.BuildHub(ctx, contracts.Default.Gateways(), os.Getenv)
	if err != nil {
		return host.Deps{}, err
	}
	set, ok := hub.First()
	if !ok {
		return host.Deps{}, fmt.Errorf("no gateway built")
	}
	return set, nil
}

// scanFlag returns the value of --name / -name (space- or =-separated) from a
// raw arg slice without consuming a FlagSet, so config.json can be read before
// Parse to seed other flags' defaults. Returns def when the flag is absent.
func scanFlag(args []string, name, def string) string {
	for i, a := range args {
		for _, p := range []string{"--" + name, "-" + name} {
			if a == p {
				if i+1 < len(args) {
					return args[i+1]
				}
				return def
			}
			if v, ok := strings.CutPrefix(a, p+"="); ok {
				return v
			}
		}
	}
	return def
}
