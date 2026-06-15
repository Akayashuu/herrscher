package main

import (
	"context"
	"flag"
	"os"
	"strings"

	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/internal/config"
	"github.com/Akayashuu/herrscher/internal/serve"
	"github.com/Akayashuu/herrscher/internal/state"
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

	statePath := fs.String("state", serve.DefaultStatePath(), "path to the daemon state file")
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
		return dctl.ErrDisabled
	}
	// Owner: env DCTL_OWNER_ID wins over config.json (the owner id is kept in
	// env alongside the token), then config seeds it for declarative setups.
	owner := or(os.Getenv("DCTL_OWNER_ID"), cfg.Owner)
	var home *state.HomeRef
	if cfg.Home != nil && cfg.Home.ID != "" {
		home = &state.HomeRef{ID: cfg.Home.ID, Type: cfg.Home.Type}
	}
	return serve.Run(ctx, c, serve.Options{
		StatePath:     *statePath,
		DefaultCmd:    *defaultCmd,
		DefaultInit:   cfg.InitPrompts,
		HealthAddr:    *healthAddr,
		StatusChannel: *statusChannel,
		InstanceID:    *instanceID,
		Token:         token,
		Owner:         owner,
		Home:          home,
		Workspace:     cfg.Workspace,
		Source:        cfg.Source,
	})
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
