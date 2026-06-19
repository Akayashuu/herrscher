package bridge

import (
	"context"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// recordSink captures the events runHub emits.
type recordSink struct{ events []contracts.Event }

func (s *recordSink) Emit(e contracts.Event) { s.events = append(s.events, e) }

// fakeBackend emits one text event (and optionally a terminal result carrying
// cost) then returns a fixed reply.
type fakeBackend struct {
	reply string
	cost  float64
}

func (b fakeBackend) Respond(_ context.Context, _ contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	if onEvent != nil {
		onEvent(contracts.BackendEvent{Kind: "text", Detail: "thinking"})
		if b.cost > 0 {
			onEvent(contracts.BackendEvent{Kind: "result", Cost: b.cost})
		}
	}
	return b.reply, nil
}
func (fakeBackend) Close() error { return nil }

// TestRunHubReplyCarriesCost proves the terminal result's cost is captured and
// attached to the reply{done} (the result event itself carries no transcript and
// is dropped), so the hub can render cost in the progress summary.
func TestRunHubReplyCarriesCost(t *testing.T) {
	sink := &recordSink{}
	in := make(chan contracts.Event, 1)
	in <- contracts.Event{T: "input", Text: "go"}
	close(in)

	runHubTurns(context.Background(), in, sink, fakeBackend{reply: "ok", cost: 0.0042}, nil)

	last := sink.events[len(sink.events)-1]
	if last.T != "reply" || !last.Done || last.Cost != 0.0042 {
		t.Fatalf("reply must carry cost 0.0042; got %+v", last)
	}
	for _, e := range sink.events {
		if e.T == "result" {
			t.Fatalf("result event must not be forwarded over the bus; got %+v", e)
		}
	}
}

func TestRunHubOneTurn(t *testing.T) {
	sink := &recordSink{}
	in := make(chan contracts.Event, 2)
	in <- contracts.Event{T: "input", Who: "alice", Text: "refactor"}
	close(in)

	be := fakeBackend{reply: "done · 4 files"}
	runHubTurns(context.Background(), in, sink, be, nil)

	want := []contracts.Event{
		{T: "chunk", Text: "thinking"},
		{T: "reply", Text: "done · 4 files", Done: true},
	}
	if len(sink.events) != len(want) {
		t.Fatalf("emitted %+v, want %+v", sink.events, want)
	}
	for i := range want {
		if sink.events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, sink.events[i], want[i])
		}
	}
}

func TestRunHubEmptyReplyStillTerminates(t *testing.T) {
	sink := &recordSink{}
	in := make(chan contracts.Event, 1)
	in <- contracts.Event{T: "input", Text: "noop"}
	close(in)

	runHubTurns(context.Background(), in, sink, fakeBackend{reply: ""}, nil)

	if len(sink.events) == 0 || sink.events[len(sink.events)-1] != (contracts.Event{T: "reply", Done: true}) {
		t.Fatalf("empty reply must still emit a terminal reply{done}; got %+v", sink.events)
	}
}

func TestRunHubContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := make(chan contracts.Event) // never fed
	done := make(chan struct{})
	go func() { runHubTurns(ctx, in, &recordSink{}, fakeBackend{}, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runHubTurns did not return on cancelled context")
	}
}
