package session

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// choiceOption is one selectable option in a Claude TUI prompt: Key is the digit
// to type to pick it, Label is the human text shown beside it.
type choiceOption struct {
	Key   string
	Label string
}

// choicePrompt is a parsed interactive prompt awaiting a numbered selection
// (e.g. a tool-permission "Do you want to proceed?"). Question is the prompt line
// above the options; Options is ordered as rendered.
type choicePrompt struct {
	Question string
	Options  []choiceOption
}

// selectorGlyphs are the cursors the TUI draws beside the highlighted option.
const selectorGlyphs = "❯>›▶"

// optionRe matches one numbered option line after box chrome is stripped, with an
// optional leading selector glyph: "❯ 1. Yes" / "  2. No, and tell Claude…".
var optionRe = regexp.MustCompile(`^[` + selectorGlyphs + `\s]*(\d+)\.\s+(\S.*)$`)

// unboxLine strips a captured line's box-drawing frame ("│ … │") and surrounding
// whitespace so the inner text can be matched directly.
func unboxLine(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "│")
	t = strings.TrimSuffix(t, "│")
	return strings.TrimSpace(t)
}

// parseChoicePrompt detects an interactive numbered prompt in a raw pane capture
// and returns its question and options. It reports false unless it finds a block
// of at least two options numbered 1,2,… AND at least one of those lines carries
// a selector glyph — the glyph is what distinguishes a live prompt from an
// ordinary numbered list in Claude's prose, so prose never false-positives.
func parseChoicePrompt(capture string) (choicePrompt, bool) {
	lines := strings.Split(capture, "\n")
	var opts []choiceOption
	var startIdx int     // index of the first option line (for locating the question)
	sawSelector := false // a glyph appeared next to some option in this block
	for i, raw := range lines {
		inner := unboxLine(raw)
		m := optionRe.FindStringSubmatch(inner)
		if m == nil {
			if len(opts) > 0 {
				break // the option block ended; stop at the first gap
			}
			continue
		}
		// Options must be consecutive 1,2,3… — a stray "2. foo" in prose won't form
		// a run starting at 1, so it can't be mistaken for a prompt.
		if want := strconv.Itoa(len(opts) + 1); m[1] != want {
			if len(opts) > 0 {
				break
			}
			continue
		}
		if len(opts) == 0 {
			startIdx = i
		}
		if strings.ContainsAny(strings.TrimSpace(raw), selectorGlyphs) {
			sawSelector = true
		}
		opts = append(opts, choiceOption{Key: m[1], Label: strings.TrimSpace(m[2])})
	}
	if len(opts) < 2 || !sawSelector {
		return choicePrompt{}, false
	}
	return choicePrompt{Question: questionAbove(lines, startIdx), Options: opts}, true
}

// questionAbove returns the nearest non-empty, non-chrome line above the options
// block — the prompt's question (e.g. "Do you want to proceed?"). Empty if none.
func questionAbove(lines []string, startIdx int) string {
	for i := startIdx - 1; i >= 0; i-- {
		t := unboxLine(lines[i])
		if t == "" || strings.TrimLeft(t, "─ ") == "" {
			continue
		}
		return t
	}
	return ""
}

// renderChoice turns a parsed prompt into plain Discord text: the question, the
// numbered options, and a hint that a numeric reply selects one. This is the
// fallback rendering used in every mode — a numeric reply flows through the
// normal turn path and is typed into the pane, which the TUI reads as the
// selection (daemon mode additionally offers a native select menu).
func renderChoice(p choicePrompt) string {
	var b strings.Builder
	if p.Question != "" {
		b.WriteString(p.Question)
		b.WriteByte('\n')
	}
	for _, o := range p.Options {
		fmt.Fprintf(&b, "%s. %s\n", o.Key, o.Label)
	}
	b.WriteString("\n_Reply with a number to choose._")
	return b.String()
}
