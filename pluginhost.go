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
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

func runPluginHost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin-host", flag.ContinueOnError)
	category := fs.String("category", "", "plugin category to host (e.g. memory)")
	natsURL := fs.String("nats", nats.DefaultURL, "NATS server URL")
	instanceID := fs.String("instance", "", "process instance id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *category != string(contracts.CategoryMemory) {
		return fmt.Errorf("plugin-host: unsupported category %q", *category)
	}

	var (
		real     contracts.Memory
		manifest contracts.Manifest
	)
	for _, p := range contracts.Default.Memories() {
		if p.Memory == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return err
		}
		if real, err = p.Memory(ctx, cfg); err != nil {
			return err
		}
		manifest = p.Manifest
		break
	}
	if real == nil {
		return fmt.Errorf("plugin-host: no memory plugin registered")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	transport.RegisterMemorySkeleton(s, real)

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
