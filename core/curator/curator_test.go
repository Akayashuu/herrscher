package curator

import (
	"context"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
)

// fakeMem is a minimal in-memory contracts.Memory for testing the curator.
type fakeMem struct{ nodes map[string]contracts.Node }

func newFake() *fakeMem { return &fakeMem{nodes: map[string]contracts.Node{}} }

func (f *fakeMem) Record(_ context.Context, n contracts.Node) error {
	f.nodes[n.Key] = n
	return nil
}
func (f *fakeMem) Recall(_ context.Context, key string, _ int) (contracts.Subgraph, error) {
	n, ok := f.nodes[key]
	if !ok {
		return contracts.Subgraph{}, errNotFound
	}
	return contracts.Subgraph{Root: n}, nil
}
func (f *fakeMem) Search(context.Context, contracts.Query) ([]contracts.Node, error) { return nil, nil }
func (f *fakeMem) Links(context.Context, string, string, string) error              { return nil }
func (f *fakeMem) Close() error                                                     { return nil }

var errNotFound = &notFound{}

type notFound struct{}

func (*notFound) Error() string { return "not found" }

func TestNilCuratorIsNoOp(t *testing.T) {
	if c := New(nil, "s"); c != nil {
		t.Fatalf("New(nil, …) should be a no-op nil")
	}
	if c := New(newFake(), ""); c != nil {
		t.Fatalf("New(…, \"\") should be a no-op nil")
	}
	var c *Curator
	if got := c.Context(context.Background()); got != "" {
		t.Fatalf("nil Context = %q, want empty", got)
	}
	if err := c.Observe(context.Background(), contracts.Prompt{}, "x"); err != nil {
		t.Fatalf("nil Observe = %v, want nil", err)
	}
}

func TestObserveThenContextHasContinuity(t *testing.T) {
	mem := newFake()
	c := New(mem, "alpha")
	ctx := context.Background()
	if got := c.Context(ctx); got != "" {
		t.Fatalf("first turn Context should be empty, got %q", got)
	}
	_ = c.Observe(ctx, contracts.Prompt{Author: "leo", Content: "deploy please"}, "done ✅")
	got := c.Context(ctx)
	if !strings.Contains(got, "leo: deploy please") || !strings.Contains(got, "done ✅") {
		t.Fatalf("recalled context missing the turn: %q", got)
	}
	if !strings.Contains(got, "session alpha") {
		t.Fatalf("recalled context missing the session title: %q", got)
	}
}

func TestObserveKeepsRollingTranscriptNewestFirst(t *testing.T) {
	mem := newFake()
	c := New(mem, "alpha")
	ctx := context.Background()
	_ = c.Observe(ctx, contracts.Prompt{Author: "a", Content: "one"}, "r1")
	_ = c.Observe(ctx, contracts.Prompt{Author: "a", Content: "two"}, "r2")
	body := mem.nodes["sessions/alpha"].Body
	lines := strings.Split(body, "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "two") || !strings.Contains(lines[1], "one") {
		t.Fatalf("want newest-first transcript [two, one], got %q", body)
	}
}

func TestObserveBoundsTranscript(t *testing.T) {
	mem := newFake()
	c := New(mem, "alpha")
	ctx := context.Background()
	for i := 0; i < maxTurns+5; i++ {
		_ = c.Observe(ctx, contracts.Prompt{Author: "a", Content: "msg"}, "r")
	}
	if got := len(strings.Split(mem.nodes["sessions/alpha"].Body, "\n")); got != maxTurns {
		t.Fatalf("transcript not bounded: %d lines, want %d", got, maxTurns)
	}
}
