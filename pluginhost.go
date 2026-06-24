package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/Herrscherd/herrscher/core/host"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

func runPluginHost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin-host", flag.ContinueOnError)
	category := fs.String("category", "", "plugin category to host (e.g. memory, orchestrator)")
	natsURL := fs.String("nats", nats.DefaultURL, "NATS server URL")
	instanceID := fs.String("instance", "", "process instance id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !host.SupportedRemoteCategory(contracts.Category(*category)) {
		return fmt.Errorf("plugin-host: unsupported category %q", *category)
	}

	// Fail fast and fail closed: a half-configured TLS env refuses to start
	// rather than silently serving plaintext off-loopback. Validated before any
	// plugin is built so a misconfiguration never reaches I/O. No TLS env set =>
	// plaintext on the (default loopback) bind, exactly as before.
	tlsCfg := tlsConfigFromEnv()
	if err := tlsCfg.Validate(); err != nil {
		return fmt.Errorf("plugin-host: %w", err)
	}

	// SupportedRemoteCategory gates *category above; here we build the real
	// plugin for it and select its skeleton. Each remote category serves a
	// different port over the shared Plugin service, so the build+register pair
	// branches on *category — adding a category is one case, not a new host.
	var (
		manifest  contracts.Manifest
		register  func(*grpc.Server)
		closeReal func()
	)
	switch contracts.Category(*category) {
	case contracts.CategoryMemory:
		real, m, err := firstMemory(ctx)
		if err != nil {
			return err
		}
		manifest, register, closeReal = m,
			func(s *grpc.Server) { transport.RegisterMemorySkeleton(s, real) },
			func() { _ = real.Close() }
	case contracts.CategoryOrchestrator:
		real, m, err := firstOrchestrator(ctx)
		if err != nil {
			return err
		}
		manifest, register, closeReal = m,
			func(s *grpc.Server) { transport.RegisterOrchestratorSkeleton(s, real) },
			func() { _ = real.Close() }
	case contracts.CategoryBackend:
		real, m, err := firstBackend(ctx)
		if err != nil {
			return err
		}
		manifest, register, closeReal = m,
			func(s *grpc.Server) { transport.RegisterBackendSkeleton(s, real) },
			func() { _ = real.Close() }
	default:
		return fmt.Errorf("plugin-host: category %q passed the support gate but has no skeleton", *category)
	}
	// The plugin-host owns the real plugin: it builds it once, serves it to every
	// client, and closes it once on shutdown. Client proxies only close their own
	// gRPC connection, never the shared object.
	defer closeReal()

	// Local cancel so the heartbeat and GracefulStop goroutines also unwind when
	// Serve returns on its own (e.g. a listener error), not only on parent ctx.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lis, err := net.Listen("tcp", bindAddr())
	if err != nil {
		return err
	}
	var s *grpc.Server
	if tlsCfg.Enabled() {
		creds, err := tlsCfg.ServerCredentials()
		if err != nil {
			return fmt.Errorf("plugin-host: %w", err)
		}
		s = grpc.NewServer(grpc.Creds(creds))
	} else {
		s = grpc.NewServer()
	}
	register(s)

	nc, err := nats.Connect(*natsURL)
	if err != nil {
		return err
	}
	defer nc.Close()
	ann := transport.Announcement{Manifest: manifest, GrpcAddr: lis.Addr().String(), InstanceID: *instanceID}

	// NATS core pub-sub has no replay: re-announce so a resolver subscribing later still finds us.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			_ = transport.Announce(nc, ann)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	go func() { <-ctx.Done(); s.GracefulStop() }()
	return s.Serve(lis)
}

// firstMemory builds the first registered memory plugin from its resolved env
// config, returning it with its manifest for announcement.
func firstMemory(ctx context.Context) (contracts.Memory, contracts.Manifest, error) {
	for _, p := range contracts.Default.Memories() {
		if p.Memory == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("memory plugin-host: %w", err)
		}
		m, err := p.Memory(ctx, cfg)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("memory plugin-host: %w", err)
		}
		return m, p.Manifest, nil
	}
	return nil, contracts.Manifest{}, fmt.Errorf("plugin-host: no memory plugin registered")
}

// firstOrchestrator builds the first registered orchestrator plugin. The remote
// orchestrator runs without the host's in-process Memory or the
// session/scope/learn config bag (those are bridge-owned runtime state): C2
// ships the request/response proxy, not remote scope threading.
func firstOrchestrator(ctx context.Context) (contracts.Orchestrator, contracts.Manifest, error) {
	for _, p := range contracts.Default.Orchestrators() {
		if p.Orchestrator == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("orchestrator plugin-host: %w", err)
		}
		o, err := p.Orchestrator(ctx, cfg, nil)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("orchestrator plugin-host: %w", err)
		}
		return o, p.Manifest, nil
	}
	return nil, contracts.Manifest{}, fmt.Errorf("plugin-host: no orchestrator plugin registered")
}

// firstBackend builds the first registered backend plugin from its resolved env
// config. The streaming skeleton forwards its per-turn event stream to remote
// hosts, preserving ordering and the reply{done} boundary.
func firstBackend(ctx context.Context) (contracts.Backend, contracts.Manifest, error) {
	for _, p := range contracts.Default.Backends() {
		if p.Backend == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("backend plugin-host: %w", err)
		}
		b, err := p.Backend(ctx, cfg)
		if err != nil {
			return nil, contracts.Manifest{}, fmt.Errorf("backend plugin-host: %w", err)
		}
		return b, p.Manifest, nil
	}
	return nil, contracts.Manifest{}, fmt.Errorf("plugin-host: no backend plugin registered")
}
