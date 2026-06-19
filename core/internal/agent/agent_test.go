package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeWritesFilesWithSubstitution(t *testing.T) {
	s := NewStore(t.TempDir())
	a, err := s.Create(CreateSpec{Name: "roblox", Soul: "PERSONA", MCP: "neublox serve --project {{WORKTREE}}"})
	if err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := a.Materialize(wt); err != nil {
		t.Fatal(err)
	}

	mcp, _ := os.ReadFile(filepath.Join(wt, ".mcp.json"))
	if strings.Contains(string(mcp), "{{WORKTREE}}") {
		t.Fatalf("token not substituted:\n%s", mcp)
	}
	if !strings.Contains(string(mcp), wt) {
		t.Fatalf("worktree path not injected:\n%s", mcp)
	}
	if !strings.Contains(string(mcp), `"neublox"`) {
		t.Fatalf("neublox server missing:\n%s", mcp)
	}

	settings, _ := os.ReadFile(filepath.Join(wt, ".claude", "settings.json"))
	if !strings.Contains(string(settings), "enableAllProjectMcpServers") {
		t.Fatalf("settings missing:\n%s", settings)
	}

	claude, _ := os.ReadFile(filepath.Join(wt, ".claude", "CLAUDE.md"))
	if string(claude) != "PERSONA" {
		t.Fatalf("CLAUDE.md = %q", claude)
	}
}

func TestMaterializeMissingHomeErrors(t *testing.T) {
	a := Agent{Name: "ghost", Home: filepath.Join(t.TempDir(), "ghost")}
	if err := a.Materialize(t.TempDir()); err == nil {
		t.Fatal("expected error materializing an agent with no home files")
	}
}
