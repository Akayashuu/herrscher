package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Herrscherd/dctl"
	claude "github.com/Herrscherd/herrscher-claude-backend"
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

func runServe(ctx context.Context, c *dctl.Client, token string, args []string) error {
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
		// Load secrets in Go rather than via a shell/batch wrapper, then rebuild
		// the client from the now-populated environment (main built its client
		// before this file was read).
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
		token = os.Getenv("DISCORD_BOT_TOKEN")
		c = dctl.New(token, os.Getenv("DISCORD_CHANNEL_ID"))
	}
	if !c.Enabled() {
		return fmt.Errorf("discord: bot token disabled (set DISCORD_BOT_TOKEN)")
	}
	// Owner: env DCTL_OWNER_ID wins over config.json (the owner id is kept in
	// env alongside the token), then config seeds it for declarative setups.
	owner := or(os.Getenv("DCTL_OWNER_ID"), cfg.Owner)
	var home *host.HomeRef
	if cfg.Home != nil && cfg.Home.ID != "" {
		home = &host.HomeRef{ID: cfg.Home.ID, Type: cfg.Home.Type}
	}

	// Registry-driven wiring: the daemon instantiates the gateway from the plugin
	// registry rather than hand-wiring Discord. Each plugin self-registered into
	// contracts.Default from its init() (blank import in plugins.go); here we build
	// the first gateway's GatewaySet from runtime config. Adding a gateway is a
	// blank import + rebuild — no code change here.
	deps, err := buildGateway(ctx, contracts.PluginConfig{Settings: map[string]string{
		"token":   token,
		"channel": os.Getenv("DISCORD_CHANNEL_ID"),
	}})
	if err != nil {
		return err
	}
	deps.Gateway = contracts.Degrade(deps.Gateway)
	// The cmd autocomplete catalog is model-specific, so the binary supplies it
	// from the claude backend — core stays agnostic.
	bin := *defaultCmd
	if f := strings.Fields(*defaultCmd); len(f) > 0 {
		bin = f[0]
	}
	return host.Run(ctx, deps, host.Options{
		StatePath:     *statePath,
		DefaultCmd:    *defaultCmd,
		HealthAddr:    *healthAddr,
		StatusChannel: *statusChannel,
		InstanceID:    *instanceID,
		Owner:         owner,
		Home:          home,
		Workspace:     cfg.Workspace,
		Source:        cfg.Source,
		CmdPresets:    claude.CommandPresets(bin),
	})
}

// buildGateway instantiates the first registered gateway plugin's GatewaySet
// from cfg. It iterates contracts.Default.Gateways() so the daemon is driven by
// the registry, not by hand-wired plugin types: a blank import in plugins.go is
// all it takes for a new gateway to be picked up here.
func buildGateway(ctx context.Context, cfg contracts.PluginConfig) (host.Deps, error) {
	for _, p := range contracts.Default.Gateways() {
		if p.Gateway == nil {
			continue
		}
		set, err := p.Gateway(ctx, cfg)
		if err != nil {
			return host.Deps{}, fmt.Errorf("gateway %q: %w", p.Manifest.Kind, err)
		}
		return set, nil
	}
	return host.Deps{}, fmt.Errorf("no gateway plugin registered")
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
