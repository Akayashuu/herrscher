package serve

import (
	"testing"

	"github.com/Akayashuu/herrscher/contracts"
)

func TestBuildRegistryHasDiscordGateway(t *testing.T) {
	var r contracts.Registry
	registerPlugins(&r, nil) // nil client is fine: we only inspect manifests
	gws := r.Gateways()
	if len(gws) != 1 {
		t.Fatalf("expected 1 gateway, got %d", len(gws))
	}
	if gws[0].Manifest().Kind != "discord" {
		t.Fatalf("expected discord gateway, got %q", gws[0].Manifest().Kind)
	}
}
