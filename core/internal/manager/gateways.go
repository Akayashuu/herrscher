package manager

import "strings"

// ParseGateways turns the `--gateways a,b` flag (and the `--terminal-only`
// shorthand) into the ordered, de-duplicated gateway set stored on a session.
// An explicit list always wins; an empty list with terminalOnly yields
// ["terminal"]; an empty list otherwise defaults to ["discord"].
func ParseGateways(list string, terminalOnly bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) > 0 {
		return out
	}
	if terminalOnly {
		return []string{"terminal"}
	}
	return []string{"discord"}
}
