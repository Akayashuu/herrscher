package host

import (
	"context"
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// GatewayHub holds every registered gateway plugin instantiated into a
// GatewaySet, keyed by Manifest.Kind. It is the core's multi-gateway port: the
// daemon resolves a session's bound gateways through it instead of hand-wiring a
// single gateway. Kinds() preserves registration order.
type GatewayHub struct {
	sets  map[string]contracts.GatewaySet
	order []string
}

// BuildHub instantiates each gateway plugin in plugins. A plugin whose config
// can't resolve, or whose factory errors, is skipped (its required vars are
// absent — e.g. no Discord token — which must not stop other gateways from
// running). If NO gateway builds, the aggregated per-gateway reasons are
// returned so a single-gateway stack still fails fast with a clear message.
func BuildHub(ctx context.Context, plugins []contracts.Plugin, getenv func(string) string) (*GatewayHub, error) {
	h := &GatewayHub{sets: map[string]contracts.GatewaySet{}}
	var failures []string
	for _, p := range plugins {
		if p.Gateway == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, getenv)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Manifest.Kind, err))
			continue
		}
		set, err := p.Gateway(ctx, cfg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Manifest.Kind, err))
			continue
		}
		if _, dup := h.sets[p.Manifest.Kind]; !dup {
			h.order = append(h.order, p.Manifest.Kind)
		}
		h.sets[p.Manifest.Kind] = set
	}
	if len(h.sets) == 0 {
		if len(failures) == 0 {
			return nil, fmt.Errorf("no gateway plugin registered")
		}
		return nil, fmt.Errorf("no gateway available: %s", strings.Join(failures, "; "))
	}
	return h, nil
}

// Kinds returns the built gateway kinds in registration order.
func (h *GatewayHub) Kinds() []string { return append([]string(nil), h.order...) }

// Get returns the GatewaySet for a kind and whether it was built.
func (h *GatewayHub) Get(kind string) (contracts.GatewaySet, bool) {
	s, ok := h.sets[kind]
	return s, ok
}

// First returns the first built gateway set (registration order). It preserves
// the pre-hub "first registered gateway" behavior for callers not yet
// gateway-aware.
func (h *GatewayHub) First() contracts.GatewaySet { return h.sets[h.order[0]] }
