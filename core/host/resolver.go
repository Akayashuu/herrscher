package host

import (
	"context"
	"errors"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/nats-io/nats.go"
)

// Resolver turns registered plugins into live port objects, choosing local
// (in-proc factory) or remote (gRPC proxy) per category. With a nil/empty
// remote set every category resolves local — today's behaviour.
type Resolver struct {
	remote  map[contracts.Category]bool
	NatsURL string // "" => nats.DefaultURL; consulted only on the remote path
}

func NewResolver(remote map[contracts.Category]bool, natsURL string) *Resolver {
	return &Resolver{remote: remote, NatsURL: natsURL}
}

func (r *Resolver) isRemote(c contracts.Category) bool {
	return r.remote != nil && r.remote[c]
}

// Memory resolves the first registered memory plugin. Local: call the factory.
// Remote: dial a gRPC proxy via NATS announcements. Returns nil (no error) when
// none is registered — memory stays optional, matching buildMemory's contract.
func (r *Resolver) Memory(ctx context.Context, plugins []contracts.Plugin, getenv func(string) string) (contracts.Memory, error) {
	for _, p := range plugins {
		if p.Memory == nil {
			continue
		}
		if r.isRemote(contracts.CategoryMemory) {
			return r.dialRemoteMemory(ctx, p)
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, getenv)
		if err != nil {
			return nil, err
		}
		return p.Memory(ctx, cfg)
	}
	return nil, nil
}

func (r *Resolver) natsURL() string {
	if r.NatsURL != "" {
		return r.NatsURL
	}
	return nats.DefaultURL
}

func (r *Resolver) dialRemoteMemory(ctx context.Context, _ contracts.Plugin) (contracts.Memory, error) {
	nc, err := nats.Connect(r.natsURL())
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	reg := transport.NewRemoteRegistry()
	seen := make(chan struct{}, 1)
	if err := transport.WatchAnnouncements(nc, func(a transport.Announcement) {
		reg.Observe(a)
		select {
		case seen <- struct{}{}:
		default:
		}
	}); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if mems := reg.Memories(); len(mems) > 0 {
			return transport.DialMemory(ctx, mems[0])
		}
		select {
		case <-seen:
		case <-deadline.C:
			return nil, errors.New("resolver: no remote memory announced within 10s")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
