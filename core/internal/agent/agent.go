// Package agent models a durable companion agent: a persistent home directory
// holding the agent's persona (SOUL.md), its MCP server declaration (mcp.json),
// and its Claude settings (settings.json). The agent is materialized into a
// disposable session worktree by Agent.Materialize, which copies those files
// into the worktree as the files Claude Code auto-reads when its cwd is the
// worktree (.claude/CLAUDE.md, .mcp.json, .claude/settings.json). The model is
// domain-neutral: callers (e.g. Neublox's Roblox profile) supply the persona and
// MCP server; the package only stores and materializes them.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File names inside an agent home (the durable source of truth).
const (
	soulFile     = "SOUL.md"
	mcpFile      = "mcp.json"
	settingsFile = "settings.json"
)

// worktreeToken is replaced with the absolute worktree path when an agent is
// materialized, so an agent's mcp.json can point a server at the session's
// working directory without knowing it in advance.
const worktreeToken = "{{WORKTREE}}"

// Agent is a durable companion: a name and the home directory that stores its
// persona and provisioning files.
type Agent struct {
	Name string
	Home string // absolute path to the agent's home directory
}

// Materialize provisions the agent into a session worktree by writing the three
// files Claude Code reads from its working directory:
//
//	<worktree>/.mcp.json             (from <home>/mcp.json)
//	<worktree>/.claude/settings.json (from <home>/settings.json)
//	<worktree>/.claude/CLAUDE.md     (from <home>/SOUL.md — the layered persona)
//
// Any worktreeToken in a source file is replaced with the worktree path.
func (a Agent) Materialize(worktree string) error {
	claudeDir := filepath.Join(worktree, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	copies := []struct{ src, dst string }{
		{filepath.Join(a.Home, mcpFile), filepath.Join(worktree, ".mcp.json")},
		{filepath.Join(a.Home, settingsFile), filepath.Join(claudeDir, "settings.json")},
		{filepath.Join(a.Home, soulFile), filepath.Join(claudeDir, "CLAUDE.md")},
	}
	for _, c := range copies {
		buf, err := os.ReadFile(c.src)
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(c.src), err)
		}
		out := strings.ReplaceAll(string(buf), worktreeToken, worktree)
		if err := os.WriteFile(c.dst, []byte(out), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", c.dst, err)
		}
	}
	return nil
}
