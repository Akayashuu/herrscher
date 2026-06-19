package host

import (
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestProgressRenderCapsLines(t *testing.T) {
	pv := newProgressView(nil, "full", false, time.Now())
	for i := 0; i < 20; i++ {
		pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "f.go"})
	}
	out := pv.render()
	if !strings.HasPrefix(out, "⏳ en cours…\n") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "…\n") {
		t.Fatal("expected elision marker for >maxLines")
	}
	if n := strings.Count(out, "\n"); n != maxLines+1 {
		t.Fatalf("line count = %d, want %d", n, maxLines+1)
	}
}

func TestProgressActionsLevelDropsText(t *testing.T) {
	pv := newProgressView(nil, "actions", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "text", Detail: "thinking out loud"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "ls"})
	out := pv.render()
	if strings.Contains(out, "thinking out loud") {
		t.Fatal("actions level must drop text events")
	}
	if !strings.Contains(out, "Bash · ls") {
		t.Fatalf("expected Bash line, got %q", out)
	}
}

func TestProgressSummary(t *testing.T) {
	pv := newProgressView(nil, "full", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "a"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "b"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "x"})
	pv.add(contracts.BackendEvent{Kind: "text", Detail: "noise"})
	pv.add(contracts.BackendEvent{Kind: "result", Cost: 0.04})
	s := pv.summary(false)
	if !strings.HasPrefix(s, "✅ 3 actions (Bash×2, Read)") {
		t.Fatalf("summary = %q", s)
	}
	if !strings.Contains(s, "$0.04") {
		t.Fatalf("expected cost in summary: %q", s)
	}
}

func TestProgressSummaryError(t *testing.T) {
	pv := newProgressView(nil, "full", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "a"})
	if s := pv.summary(true); !strings.HasPrefix(s, "⚠️ 1 action") {
		t.Fatalf("summary = %q", s)
	}
}

func TestProgressSummaryNoActions(t *testing.T) {
	// A full-mode turn that only reasoned (text, no tools) must not read "0 actions".
	pv := newProgressView(nil, "full", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "text", Detail: "just thinking"})
	pv.add(contracts.BackendEvent{Kind: "result", Cost: 0})
	s := pv.summary(false)
	if !strings.HasPrefix(s, "✅ terminé") {
		t.Fatalf("summary = %q, want ✅ terminé…", s)
	}
	if strings.Contains(s, "action") {
		t.Fatalf("zero-tool summary should not mention actions: %q", s)
	}
}

func TestProgressResetDiscardsPartialTurn(t *testing.T) {
	// A mid-turn crash emits partial events, then a reset, then the retried turn.
	// Only the retried turn must be counted.
	pv := newProgressView(nil, "full", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "a"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "b"})
	pv.add(contracts.BackendEvent{Kind: "reset"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "c"})
	pv.add(contracts.BackendEvent{Kind: "result", Cost: 0.02})
	s := pv.summary(false)
	if !strings.HasPrefix(s, "✅ 1 action (Read)") {
		t.Fatalf("reset must discard the partial turn, got %q", s)
	}
}

func TestProgressFinishCollapsesOrphanAfterReset(t *testing.T) {
	// A turn streams activity (posts a live message), then a reset clears it and
	// the retry produces nothing in actions mode: finish must still collapse the
	// orphaned live message to a summary rather than leave it on "en cours".
	var posts []string
	post := func(id, content string) (string, error) {
		posts = append(posts, content)
		return "msg-1", nil
	}
	pv := newProgressView(post, "actions", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "a"})
	pv.add(contracts.BackendEvent{Kind: "reset"})
	pv.finish(false)
	if len(posts) == 0 || !strings.HasPrefix(posts[len(posts)-1], "✅") {
		t.Fatalf("expected orphan collapsed to summary, posts = %v", posts)
	}
}

func TestPlural(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "s"}, {1, ""}, {2, "s"}} {
		if got := plural(tc.n); got != tc.want {
			t.Fatalf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestProgressPostsThrottledThenFlushes(t *testing.T) {
	var posts []string
	post := func(id, content string) (string, error) {
		posts = append(posts, content)
		return "msg-1", nil
	}
	pv := newProgressView(post, "full", false, time.Now())
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Bash", Detail: "a"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "b"})
	if len(posts) != 1 {
		t.Fatalf("expected 1 throttled post, got %d", len(posts))
	}
	pv.finish(false)
	if len(posts) != 2 {
		t.Fatalf("expected final flush, got %d posts", len(posts))
	}
	if !strings.HasPrefix(posts[1], "✅") {
		t.Fatalf("final post should be summary, got %q", posts[1])
	}
}
