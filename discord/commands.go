package discord

import (
	"context"

	"github.com/Akayashuu/dctl"
)

// Commands is the declarative slash-command set for the Discord gateway.
func Commands() []map[string]any {
	const (
		typeSub   = 1
		typeGroup = 2
		typeStr   = 3
		typeBool  = 5
		typeUser  = 6
		typeChan  = 7
	)
	return []map[string]any{
		{"name": "set", "description": "dctl settings", "options": []map[string]any{
			{"name": "home", "description": "Set the category/forum holding sessions", "type": typeSub,
				"options": []map[string]any{
					{"name": "channel", "description": "Category or forum", "type": typeChan, "required": true},
				}},
			{"name": "workspace", "description": "Set the workspace root holding projects", "type": typeSub,
				"options": []map[string]any{
					{"name": "path", "description": "Absolute path to the workspace dir", "type": typeStr, "required": true},
				}},
			{"name": "source", "description": "Set the dctl source checkout (/service update builds from it)", "type": typeSub,
				"options": []map[string]any{
					{"name": "path", "description": "Absolute path to the dctl source checkout", "type": typeStr, "required": true},
				}},
		}},
		{"name": "session", "description": "Manage Claude sessions", "options": []map[string]any{
			{"name": "create", "description": "Create a session", "type": typeSub, "options": []map[string]any{
				{"name": "name", "description": "Session name", "type": typeStr, "required": true},
				{"name": "cmd", "description": "Override bridged command", "type": typeStr, "autocomplete": true},
				{"name": "shared", "description": "Run in the main checkout (no worktree)", "type": typeBool},
				{"name": "backend", "description": "Bridge backend (default stream)", "type": typeStr, "choices": []map[string]any{
					{"name": "stream", "value": "stream"},
					{"name": "tmux", "value": "tmux"},
				}},
				{"name": "project", "description": "Workspace project to start from (see /workspace list)", "type": typeStr, "autocomplete": true},
				{"name": "clone", "description": "Remote repo to clone first (owner/name or URL)", "type": typeStr, "autocomplete": true},
				{"name": "init", "description": "tmux priming sent before your first message (separate several with ||)", "type": typeStr},
			}},
			{"name": "close", "description": "Close a session", "type": typeSub, "options": []map[string]any{
				{"name": "name", "description": "Session name", "type": typeStr, "required": true, "autocomplete": true},
				{"name": "force", "description": "Discard uncommitted worktree changes", "type": typeBool},
			}},
			{"name": "list", "description": "List active sessions", "type": typeSub},
			{"name": "allow", "description": "Per-session allowlist", "type": typeGroup, "options": []map[string]any{
				{"name": "add", "description": "Allow a user on this session", "type": typeSub, "options": []map[string]any{
					{"name": "name", "description": "Session name", "type": typeStr, "required": true},
					{"name": "user", "description": "User", "type": typeUser, "required": true},
				}},
				{"name": "remove", "description": "Remove a user from this session's allowlist", "type": typeSub, "options": []map[string]any{
					{"name": "name", "description": "Session name", "type": typeStr, "required": true},
					{"name": "user", "description": "User", "type": typeUser, "required": true},
				}},
				{"name": "list", "description": "Show this session's allowlist", "type": typeSub, "options": []map[string]any{
					{"name": "name", "description": "Session name", "type": typeStr, "required": true},
				}},
			}},
			{"name": "who", "description": "Show who has written in this session", "type": typeSub, "options": []map[string]any{
				{"name": "name", "description": "Session name", "type": typeStr, "required": true},
			}},
		}},
		{"name": "workspace", "description": "Inspect the workspace", "options": []map[string]any{
			{"name": "list", "description": "List local git projects in the workspace", "type": typeSub},
			{"name": "remotes", "description": "List remote repos via gh/glab", "type": typeSub, "options": []map[string]any{
				{"name": "forge", "description": "Limit to one forge", "type": typeStr, "choices": []map[string]any{
					{"name": "github", "value": "github"},
					{"name": "gitlab", "value": "gitlab"},
				}},
			}},
		}},
		{"name": "service", "description": "Control the dctl daemon", "options": []map[string]any{
			{"name": "restart", "description": "Restart the daemon", "type": typeSub},
			{"name": "update", "description": "Pull, rebuild from source, and restart", "type": typeSub, "options": []map[string]any{
				{"name": "no_pull", "description": "Skip git pull; build the current checkout", "type": typeBool},
			}},
		}},
		{"name": "allow", "description": "Manage the command allowlist", "options": []map[string]any{
			{"name": "add", "description": "Allow a user", "type": typeSub, "options": []map[string]any{
				{"name": "user", "description": "User", "type": typeUser, "required": true}}},
			{"name": "remove", "description": "Disallow a user", "type": typeSub, "options": []map[string]any{
				{"name": "user", "description": "User", "type": typeUser, "required": true}}},
			{"name": "list", "description": "Show the allowlist", "type": typeSub},
		}},
	}
}

// RegisterCommands registers the Discord gateway's slash commands for the sole guild.
func RegisterCommands(ctx context.Context, c *dctl.Client) error {
	return c.RegisterCommands(ctx, Commands())
}
