// Package curator drives the Memory port around a conversation turn: it recalls
// background to prime the backend before a turn and records the turn afterwards.
// It is the default, minimal implementation of the curation seam
// (contracts.CurationHook); a richer Orchestrator plugin can replace it later.
package curator

import (
	"context"
	"fmt"
	"strings"

	"github.com/Herrscherd/herrscher-contracts"
)

// maxTurns bounds the rolling transcript kept on a session node so memory never
// grows without bound for a long-lived channel.
const maxTurns = 20

// Curator weaves Memory into one session's turns. A nil *Curator is a no-op, so
// the bridge holds one unconditionally whether or not a Memory plugin is wired.
type Curator struct {
	mem     contracts.Memory
	session string // session node key
}

// New returns a Curator for session, or nil (a no-op) when no Memory is wired or
// the session is unnamed — both make memory meaningless.
func New(mem contracts.Memory, session string) *Curator {
	if mem == nil || session == "" {
		return nil
	}
	return &Curator{mem: mem, session: "sessions/" + session}
}

// Context recalls the session node and its neighbours into a compact background
// block to prepend to the next prompt. It returns "" (never a turn-breaking
// error) when nothing is recalled yet — a first turn or a missing node.
func (c *Curator) Context(ctx context.Context) string {
	if c == nil {
		return ""
	}
	sg, err := c.mem.Recall(ctx, c.session, 1)
	if err != nil {
		return ""
	}
	var b strings.Builder
	writeNode(&b, sg.Root)
	for _, n := range sg.Nodes {
		writeNode(&b, n)
	}
	return strings.TrimSpace(b.String())
}

func writeNode(b *strings.Builder, n contracts.Node) {
	if n.Title != "" {
		fmt.Fprintf(b, "## %s\n", n.Title)
	}
	if body := strings.TrimSpace(n.Body); body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
	}
}

// Observe records the turn by upserting the session node with a bounded rolling
// transcript, so the next Context call has continuity. A nil Curator is a no-op.
func (c *Curator) Observe(ctx context.Context, p contracts.Prompt, reply string) error {
	if c == nil {
		return nil
	}
	var prev string
	if sg, err := c.mem.Recall(ctx, c.session, 0); err == nil {
		prev = sg.Root.Body
	}
	body := turnLine(p.Author, p.Content, reply)
	if prev != "" {
		body += "\n" + prev
	}
	return c.mem.Record(ctx, contracts.Node{
		Key:   c.session,
		Kind:  contracts.KindSession,
		Title: "session " + strings.TrimPrefix(c.session, "sessions/"),
		Body:  capLines(body, maxTurns),
	})
}

// Consolidate satisfies contracts.CurationHook. The default curator keeps a
// bounded rolling transcript inline (see Observe) and has nothing to consolidate
// yet; a future Orchestrator plugin overrides this with summarisation/pruning.
func (c *Curator) Consolidate(ctx context.Context) error { return nil }

var _ contracts.CurationHook = (*Curator)(nil)

func turnLine(author, content, reply string) string {
	return fmt.Sprintf("- %s: %s → %s", author, oneline(content, 100), oneline(reply, 200))
}

func oneline(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// capLines keeps the first n newline-separated lines (newest-first transcript).
func capLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
