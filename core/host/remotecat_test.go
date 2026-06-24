package host

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestSupportedRemoteCategoriesAreMemoryAndOrchestrator(t *testing.T) {
	for _, c := range []contracts.Category{
		contracts.CategoryMemory, contracts.CategoryOrchestrator,
	} {
		if !SupportedRemoteCategory(c) {
			t.Fatalf("%q must be a supported remote category (C1/C2)", c)
		}
	}
	// The streaming backend (C3) and gateway are not remote-capable yet.
	for _, c := range []contracts.Category{
		contracts.CategoryBackend, contracts.CategoryGateway,
	} {
		if SupportedRemoteCategory(c) {
			t.Fatalf("%q must not be remote-supported yet", c)
		}
	}
	if n := len(supportedRemoteCategories); n != 2 {
		t.Fatalf("expected exactly two supported remote categories (memory, orchestrator), got %d: %v", n, supportedRemoteCategories)
	}
}

func TestStartRemotePluginHostsSkipsUnsupportedWithWarning(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled: no real plugin-host process should be spawned

	// An unsupported remote category must be skipped with a logged warning
	// (Stage A logger), not hosted out-of-process.
	startRemotePluginHosts(ctx, "/bin/herrscher",
		map[contracts.Category]bool{contracts.CategoryBackend: true}, log)

	out := buf.String()
	if !strings.Contains(out, "not yet supported") || !strings.Contains(out, "category=backend") {
		t.Fatalf("expected an unsupported-category warning for backend, got: %q", out)
	}
}
