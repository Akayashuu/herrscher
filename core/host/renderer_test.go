package host

import (
	"context"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestRendererPostsFinalReplyThreaded(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, g, "c1", "full")

	r.handle(context.Background(), contracts.Event{T: "human", Who: "alice", Text: "hi"})
	r.handle(context.Background(), contracts.Event{T: "status", Text: "Edit file.go"})
	r.handle(context.Background(), contracts.Event{T: "reply", Text: "done", Done: true})

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 1 || g.posted[0] != "done" {
		t.Fatalf("renderer posted %v, want one final reply 'done'", g.posted)
	}
}

func TestRendererSkipsEmptyReply(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, g, "c1", "off")
	r.handle(context.Background(), contracts.Event{T: "reply", Done: true})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 0 {
		t.Fatalf("empty reply must not post; got %v", g.posted)
	}
}

// A pending choice the backend couldn't resolve natively is collapsed into the
// reply text (numbered options); the renderer posts it as plain text. Native
// select-menu picks over the bus are a documented follow-up.
func TestRendererPostsChoiceAsText(t *testing.T) {
	g := &fanRecorder{}
	r := newGatewayRenderer(g, g, "c1", "off")
	r.handle(context.Background(), contracts.Event{T: "reply", Text: "Pick:\n1) yes\n2) no", Done: true})
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.posted) != 1 || !strings.Contains(g.posted[0], "Pick:") {
		t.Fatalf("choice text not posted: %v", g.posted)
	}
}
