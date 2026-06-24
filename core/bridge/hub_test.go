package bridge

import (
	"context"
	"errors"
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

// hangingBackend simulates a remote backend whose stream is lost mid-turn: the
// first Respond emits an event then errors (the streaming proxy surfaces a
// hangup that way); the second succeeds. It proves a stream loss ends the turn
// cleanly via the existing reply{done} path and the next queued input is still
// processed — the C3 abandonment guarantee, with no new abandonment path.
type hangingBackend struct{ calls int }

func (b *hangingBackend) Respond(_ context.Context, _ contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	b.calls++
	if b.calls == 1 {
		onEvent(contracts.BackendEvent{Kind: "text", Detail: "partial"})
		return "", errors.New("backend: stream ended before reply{done}")
	}
	return "second ok", nil
}
func (*hangingBackend) Close() error { return nil }

func TestRunHubBackendStreamLossAbandonsTurnThenContinues(t *testing.T) {
	sink := &recordSink{}
	in := make(chan contracts.Event, 2)
	in <- contracts.Event{T: "input", Text: "first"}
	in <- contracts.Event{T: "input", Text: "second"}
	close(in)

	be := &hangingBackend{}
	runHubTurns(context.Background(), in, sink, be, nil)

	var dones []contracts.Event
	for _, e := range sink.events {
		if e.T == "reply" && e.Done {
			dones = append(dones, e)
		}
	}
	if len(dones) != 2 {
		t.Fatalf("want two terminal reply{done} (turn abandoned, then next processed); got %+v", sink.events)
	}
	if be.calls != 2 {
		t.Fatalf("second input must reach the recovered backend; got %d Respond calls", be.calls)
	}
	if dones[1].Text != "second ok" {
		t.Fatalf("second turn reply = %q, want %q", dones[1].Text, "second ok")
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
