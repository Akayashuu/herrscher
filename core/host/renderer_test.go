package host

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestRendererPostsOnlyFinalReply(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, "c1")

	r.handle(context.Background(), contracts.Event{T: "human", Who: "alice", Text: "hi"})
	r.handle(context.Background(), contracts.Event{T: "status", Text: "Edit file.go"})
	r.handle(context.Background(), contracts.Event{T: "chunk", Text: "thinking"})
	r.handle(context.Background(), contracts.Event{T: "reset"})
	r.handle(context.Background(), contracts.Event{T: "reply", Text: "done", Done: true})

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 1 || g.posted[0] != "done" {
		t.Fatalf("renderer posted %v, want one final reply 'done'", g.posted)
	}
	if g.upserts != 0 || len(g.statuses) != 0 {
		t.Fatalf("renderer must not render progress; upserts=%d statuses=%v", g.upserts, g.statuses)
	}
}

func TestRendererSkipsEmptyReply(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, "c1")
	r.handle(context.Background(), contracts.Event{T: "reply", Done: true})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 0 {
		t.Fatalf("empty reply must not post; got %v", g.posted)
	}
}

// A pending choice the backend couldn't resolve natively is collapsed into the
// reply text (numbered options); the renderer posts it as plain text.
func TestRendererPostsChoiceAsText(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, "c1")
	r.handle(context.Background(), contracts.Event{T: "reply", Text: "Pick:\n1) yes\n2) no", Done: true})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 1 || !strings.Contains(g.posted[0], "Pick:") {
		t.Fatalf("choice text not posted: %v", g.posted)
	}
}

// A reply longer than the per-message limit is split into multiple posts.
func TestRendererChunksLongReply(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, "c1")
	long := strings.Repeat("x", gatewayMaxLen+50)
	r.handle(context.Background(), contracts.Event{T: "reply", Text: long, Done: true})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 2 {
		t.Fatalf("posted %d chunks, want 2", len(g.posted))
	}
}

// chunk counts and slices in rune space and prefers a newline break, so
// multibyte UTF-8 (accented French text) is never split into invalid bytes and
// the limit is honoured in characters. "é" is 2 bytes: a byte-based cut at the
// limit would land mid-rune and over-count the limit.
func TestChunkRuneSafeAndNewlinePreferred(t *testing.T) {
	input := strings.Repeat("é", gatewayMaxLen+10)
	parts := chunk(input, gatewayMaxLen)
	for i, p := range parts {
		if !utf8.ValidString(p) {
			t.Fatalf("part %d not valid UTF-8: %q", i, p)
		}
		if n := utf8.RuneCountInString(p); n > gatewayMaxLen {
			t.Fatalf("part %d has %d runes, want <= %d", i, n, gatewayMaxLen)
		}
	}
	if got := strings.Join(parts, ""); got != input {
		t.Fatalf("rejoined chunks lost runes")
	}

	// A newline past the halfway point of the window is the preferred break
	// point, and the consumed newline is not duplicated at the boundary.
	head := strings.Repeat("a", 1500)
	nl := head + "\n" + strings.Repeat("b", 1000)
	parts = chunk(nl, gatewayMaxLen)
	if len(parts) != 2 || parts[0] != head {
		t.Fatalf("newline break: got %d parts, first len=%d", len(parts), len(parts[0]))
	}
}
