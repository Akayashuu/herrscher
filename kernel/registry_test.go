package kernel

import "testing"

func TestRegistryCollectsGateways(t *testing.T) {
	var r Registry
	if len(r.Gateways()) != 0 {
		t.Fatalf("fresh registry should be empty")
	}
	g := &recGateway{manifest: Manifest{Kind: "discord", Category: CategoryGateway}}
	r.RegisterGateway(g)
	got := r.Gateways()
	if len(got) != 1 || got[0].Manifest().Kind != "discord" {
		t.Fatalf("registry did not return the registered gateway: %+v", got)
	}
}
