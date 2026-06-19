package host

import (
	"fmt"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// maxLines caps the live progress message body so it stays readable and well
// under Discord's 2000-char limit; older lines are elided with a leading "…".
const maxLines = 15

// progressInterval throttles live edits so a tool-heavy turn doesn't hammer
// Discord's per-channel edit rate limit. Events are coalesced between edits.
const progressInterval = 1500 * time.Millisecond

// progressView accumulates one turn's activity and pushes it to a single
// live-updating Discord message, then collapses it to a one-line summary.
// post creates (empty id) or edits (non-empty id) the message and returns its id.
type progressView struct {
	post     func(msgID, content string) (string, error)
	level    string // "actions" | "full"
	keep     bool
	start    time.Time
	lines    []string
	counts   map[string]int
	order    []string // tool names in first-seen order, for the summary
	cost     float64
	actions  int // running count of tool events, for the summary
	msgID    string
	lastEdit time.Time
	dirty    bool
}

func newProgressView(post func(string, string) (string, error), level string, keep bool, start time.Time) *progressView {
	return &progressView{post: post, level: level, keep: keep, start: start, counts: map[string]int{}}
}

// add records one event and flushes (throttled) if it produced a visible line.
func (p *progressView) add(ev contracts.BackendEvent) {
	switch ev.Kind {
	case "result":
		p.cost = ev.Cost
		return
	case "reset":
		// The stream died mid-turn and is being retried: discard the partial
		// turn's activity so the summary counts only the turn that succeeds.
		// msgID/lastEdit are kept so the same live message keeps being edited.
		p.lines = nil
		p.order = nil
		p.cost = 0
		p.actions = 0
		p.counts = map[string]int{}
		return
	case "text":
		if p.level != "full" {
			return
		}
		p.lines = append(p.lines, "💭 "+clip(flatten(ev.Detail), 120))
	case "tool":
		if _, seen := p.counts[ev.Tool]; !seen {
			p.order = append(p.order, ev.Tool)
		}
		p.counts[ev.Tool]++
		p.actions++
		line := emojiFor(ev.Tool) + " " + ev.Tool
		if d := clip(flatten(ev.Detail), 120); d != "" {
			line += " · " + d
		}
		p.lines = append(p.lines, line)
	default:
		return
	}
	p.dirty = true
	p.flush(false)
}

// flush posts the current view if dirty and (force or the throttle interval has
// elapsed). Best-effort: a post error is swallowed so it never blocks the reply.
func (p *progressView) flush(force bool) {
	if !p.dirty || p.post == nil {
		return
	}
	if !force && !p.lastEdit.IsZero() && time.Since(p.lastEdit) < progressInterval {
		return
	}
	id, err := p.post(p.msgID, p.render())
	if err != nil {
		return
	}
	p.msgID = id
	p.lastEdit = time.Now()
	p.dirty = false
}

// finish renders the terminal state: a collapsed one-line summary by default, or
// (keep) a final flush of the full running list.
func (p *progressView) finish(failed bool) {
	if len(p.lines) == 0 {
		// Nothing to show. If a live message was already posted (e.g. a turn
		// that streamed activity then got reset on a mid-turn restart), collapse
		// it to a summary so it isn't left stuck on "⏳ en cours…".
		if p.msgID != "" && p.post != nil {
			_, _ = p.post(p.msgID, p.summary(failed))
		}
		return
	}
	if p.keep {
		p.dirty = true
		p.flush(true)
		return
	}
	if p.post != nil {
		_, _ = p.post(p.msgID, p.summary(failed))
	}
}

func (p *progressView) render() string {
	lines := p.lines
	var b strings.Builder
	b.WriteString("⏳ en cours…\n")
	if len(lines) > maxLines {
		b.WriteString("…\n")
		lines = lines[len(lines)-maxLines:]
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

func (p *progressView) summary(failed bool) string {
	icon := "✅"
	if failed {
		icon = "⚠️"
	}
	parts := make([]string, 0, len(p.order))
	for _, name := range p.order {
		if n := p.counts[name]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, n))
		} else {
			parts = append(parts, name)
		}
	}
	// A turn can finish with no tool actions (e.g. pure reasoning/text in full
	// mode): report it as done rather than the misleading "0 actions".
	var s string
	if p.actions == 0 {
		s = icon + " terminé"
	} else {
		// actions >= 1 here, so plural only ever pluralizes a positive count.
		s = fmt.Sprintf("%s %d action%s", icon, p.actions, plural(p.actions))
		if len(parts) > 0 {
			s += " (" + strings.Join(parts, ", ") + ")"
		}
	}
	s += fmt.Sprintf(" · %ds", int(time.Since(p.start).Round(time.Second).Seconds()))
	if p.cost > 0 {
		s += " · " + formatCost(p.cost)
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func formatCost(c float64) string {
	if c < 0.01 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

func emojiFor(tool string) string {
	switch tool {
	case "Read":
		return "📖"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "✏️"
	case "Grep", "Glob":
		return "🔎"
	case "Task", "Agent":
		return "🤖"
	case "WebFetch", "WebSearch":
		return "🌐"
	case "TodoWrite":
		return "📝"
	default: // Bash and anything unrecognized
		return "🔧"
	}
}

// flatten collapses all whitespace runs (incl. newlines) into single spaces.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip truncates s to n runes, appending "…" when cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
