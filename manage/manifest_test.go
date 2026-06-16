package manage

import (
	"strings"
	"testing"
)

const sample = `package main

import (
	// herrscher:plugins
	_ "github.com/Herrscherd/herrscher-claude-backend"
	_ "github.com/Herrscherd/herrscher-discord-gateway"
	// herrscher:end
)
`

func TestListPlugins(t *testing.T) {
	mods, err := listPlugins(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 || mods[0] != "github.com/Herrscherd/herrscher-claude-backend" {
		t.Fatalf("unexpected modules: %v", mods)
	}
}

func TestAddPluginInserts(t *testing.T) {
	out, changed, err := addPlugin(sample, "github.com/acme/telegram-gateway")
	if err != nil || !changed {
		t.Fatalf("add failed: changed=%v err=%v", changed, err)
	}
	mods, _ := listPlugins(out)
	if len(mods) != 3 || mods[2] != "github.com/acme/telegram-gateway" {
		t.Fatalf("module not appended at end: %v", mods)
	}
	if !strings.Contains(out, endMarker) {
		t.Fatal("end marker lost after add")
	}
}

func TestAddPluginIdempotent(t *testing.T) {
	_, changed, err := addPlugin(sample, "github.com/Herrscherd/herrscher-discord-gateway")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("adding an existing module should be a no-op")
	}
}

func TestRemovePlugin(t *testing.T) {
	out, changed, err := removePlugin(sample, "github.com/Herrscherd/herrscher-discord-gateway")
	if err != nil || !changed {
		t.Fatalf("remove failed: changed=%v err=%v", changed, err)
	}
	mods, _ := listPlugins(out)
	if len(mods) != 1 || mods[0] != "github.com/Herrscherd/herrscher-claude-backend" {
		t.Fatalf("wrong module removed: %v", mods)
	}
}

func TestRemovePluginAbsent(t *testing.T) {
	_, changed, err := removePlugin(sample, "github.com/acme/nope")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("removing an absent module should be a no-op")
	}
}

func TestMissingMarkersError(t *testing.T) {
	if _, err := listPlugins("package main\n"); err == nil {
		t.Fatal("expected error when markers are missing")
	}
}
