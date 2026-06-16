// Package cli is the native, channel-agnostic command dispatcher. It holds
// declared contracts.Cmd values keyed by their namespace Path and resolves an
// argv invocation to one. It imports only contracts: a command's Run may close
// over anything (a Discord client, a backend), but the registry never sees it.
package cli

import (
	"context"
	"fmt"
	"sort"
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

// Dispatch resolves args to the command whose Path is the longest prefix of
// args, parses the remainder into an Input (--flag value pairs into Args; a
// flag declared as an optional param with no following value is treated as the
// bool "true"; anything left over goes to Rest), checks required params, and
// runs it. It returns the handler's output string.
func (r *Registry) Dispatch(ctx context.Context, args []string) (string, error) {
	cmd, rest := r.match(args)
	if cmd == nil {
		return "", fmt.Errorf("unknown command %q", strings.Join(args, " "))
	}
	in, err := parse(*cmd, rest)
	if err != nil {
		return "", err
	}
	return cmd.Run(ctx, in)
}

// match finds the command whose Path is the longest prefix of args.
func (r *Registry) match(args []string) (*contracts.Cmd, []string) {
	var best *contracts.Cmd
	bestLen := 0
	for i := range r.cmds {
		c := &r.cmds[i]
		if len(c.Path) > len(args) || len(c.Path) <= bestLen {
			continue
		}
		if hasPrefix(args, c.Path) {
			best = c
			bestLen = len(c.Path)
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, args[bestLen:]
}

func hasPrefix(args, path []string) bool {
	for i, p := range path {
		if args[i] != p {
			return false
		}
	}
	return true
}

func isParam(c contracts.Cmd, name string) (contracts.Param, bool) {
	for _, p := range c.Params {
		if p.Name == name {
			return p, true
		}
	}
	return contracts.Param{}, false
}

func parse(c contracts.Cmd, rest []string) (contracts.Input, error) {
	in := contracts.Input{Args: map[string]string{}}
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if !strings.HasPrefix(tok, "--") {
			in.Rest = append(in.Rest, tok)
			continue
		}
		name := strings.TrimPrefix(tok, "--")
		p, ok := isParam(c, name)
		if !ok {
			return in, fmt.Errorf("%s: unknown flag --%s", strings.Join(c.Path, " "), name)
		}
		// A value follows unless the next token is itself a flag; a valueless
		// optional flag is a bool set to "true".
		if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "--") {
			in.Args[name] = rest[i+1]
			i++
		} else if !p.Required {
			in.Args[name] = "true"
		} else {
			return in, fmt.Errorf("%s: flag --%s needs a value", strings.Join(c.Path, " "), name)
		}
	}
	for _, p := range c.Params {
		if p.Required {
			if _, ok := in.Args[p.Name]; !ok {
				return in, fmt.Errorf("%s: missing required --%s", strings.Join(c.Path, " "), p.Name)
			}
		}
	}
	return in, nil
}

// Help renders one usage line per command, sorted by path, for the root help.
func (r *Registry) Help() string {
	lines := make([]string, 0, len(r.cmds))
	for _, c := range r.cmds {
		line := "  " + strings.Join(c.Path, " ")
		for _, p := range c.Params {
			if p.Required {
				line += " --" + p.Name + " <" + p.Name + ">"
			} else {
				line += " [--" + p.Name + "]"
			}
		}
		if c.Help != "" {
			line += "  — " + c.Help
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
