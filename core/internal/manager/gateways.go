package manager

import (
	"regexp"
	"strings"
)

const maxGateways = 16

var gatewayKindRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ParseGateways turns the `--gateways a,b` flag (and the `--terminal_only`
// shorthand) into the ordered, de-duplicated gateway set stored on a session.
// Entries are lowercased and must match [a-z0-9_-]; invalid entries are dropped.
// An explicit list always wins; an empty list with terminalOnly yields
// ["terminal"]; an empty list otherwise defaults to ["discord"].
func ParseGateways(list string, terminalOnly bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(list, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] || !gatewayKindRe.MatchString(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == maxGateways {
			break
		}
	}
	if len(out) > 0 {
		return out
	}
	if terminalOnly {
		return []string{"terminal"}
	}
	return []string{"discord"}
}
