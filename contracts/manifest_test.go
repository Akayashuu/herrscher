package contracts

import "testing"

func TestManifestCarriesCapabilities(t *testing.T) {
	m := Manifest{
		Kind:         "discord",
		Category:     CategoryGateway,
		Capabilities: Capabilities{Reactions: true, SelectMenus: true, Replies: true},
	}
	if m.Category != "gateway" {
		t.Fatalf("CategoryGateway should be %q, got %q", "gateway", m.Category)
	}
	if !m.Capabilities.SelectMenus {
		t.Fatalf("capabilities not carried by manifest")
	}
}
