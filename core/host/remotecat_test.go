package host

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestSupportedRemoteCategoriesAreMemoryOrchestratorBackend(t *testing.T) {
	for _, c := range []contracts.Category{
		contracts.CategoryMemory, contracts.CategoryOrchestrator, contracts.CategoryBackend,
	} {
		if !SupportedRemoteCategory(c) {
			t.Fatalf("%q must be a supported remote category (C1/C2/C3)", c)
		}
	}
	// The gateway is not remote-capable (it owns the platform I/O loop).
	if SupportedRemoteCategory(contracts.CategoryGateway) {
		t.Fatal("gateway must not be remote-supported")
	}
	if n := len(supportedRemoteCategories); n != 3 {
		t.Fatalf("expected exactly three supported remote categories (memory, orchestrator, backend), got %d: %v", n, supportedRemoteCategories)
	}
}

func TestStartRemotePluginHostsSkipsUnsupportedWithWarning(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled: no real plugin-host process should be spawned

	// An unsupported remote category must be skipped with a logged warning
	// (Stage A logger), not hosted out-of-process. The gateway owns the platform
	// I/O loop and is never remote-capable, so it is the durable negative case.
	startRemotePluginHosts(ctx, "/bin/herrscher",
		map[contracts.Category]bool{contracts.CategoryGateway: true}, log)

	out := buf.String()
	if !strings.Contains(out, "not yet supported") || !strings.Contains(out, "category=gateway") {
		t.Fatalf("expected an unsupported-category warning for gateway, got: %q", out)
	}
}
