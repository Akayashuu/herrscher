package session

import (
	"regexp"
	"strings"
)

// toolEvent is one tool invocation parsed from a tmux pane capture.
type toolEvent struct {
	Tool   string
	Detail string
}

// toolBullets are the glyphs Claude's TUI prints before a tool-call line. The
// current build uses ⏺; older builds use ●.
const toolBullets = "⏺●"

// toolLineRe matches a tool-call bullet line after leading whitespace: the bullet,
// the tool name, an optional "(args)" group, and any number of trailing
// parenthesized groups (the TUI appends a timing suffix like "(2.3s)"). Only the
// first group is captured as the detail. The line must contain nothing else, so
// prose that merely starts with a bullet glyph ("● note: …") does not match, and
// "⎿" continuation lines never match (they carry no bullet).
var toolLineRe = regexp.MustCompile(`^[` + toolBullets + `]\s+([A-Za-z][A-Za-z0-9_.-]*)(?:\s*\(([^)]*)\))?(?:\s*\([^)]*\))*\s*$`)

// isToolLine reports whether a trimmed line is a tool-call bullet line. It is the
// single source of truth shared by parseToolEvents (the live feed) and
// stripToolBlocks (the final-reply cleaner), so the two never disagree on what
// counts as a tool line.
func isToolLine(trimmed string) bool {
	return toolLineRe.MatchString(trimmed)
}

// parseToolEvents returns the tool invocations in text, in first-seen order.
// Non-tool prose and ⎿ result lines are ignored.
func parseToolEvents(text string) []toolEvent {
	var out []toolEvent
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		m := toolLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// m[2] is safe to index: a non-participating optional group returns "" in Go.
		out = append(out, toolEvent{Tool: m[1], Detail: strings.TrimSpace(m[2])})
	}
	return out
}
