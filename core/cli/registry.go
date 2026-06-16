// Package cli is the native, channel-agnostic command dispatcher. It holds
// declared contracts.Cmd values keyed by their namespace Path and resolves an
// argv invocation to one. It imports only contracts: a command's Run may close
// over anything (a Discord client, a backend), but the registry never sees it.
package cli

import (
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// Registry collects commands and dispatches argv to them.
type Registry struct {
	cmds []contracts.Cmd
}

func key(path []string) string { return strings.Join(path, " ") }

// Add registers a command. It rejects an empty path or a duplicate path.
func (r *Registry) Add(c contracts.Cmd) error {
	if len(c.Path) == 0 {
		return fmt.Errorf("cli: command with empty path")
	}
	for _, e := range r.cmds {
		if key(e.Path) == key(c.Path) {
			return fmt.Errorf("cli: duplicate command %q", key(c.Path))
		}
	}
	r.cmds = append(r.cmds, c)
	return nil
}
