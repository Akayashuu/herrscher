package bridge

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// fakeSink collects emitted events in order.
type fakeSink struct{ events []contracts.Event }

func (s *fakeSink) Emit(e contracts.Event) { s.events = append(s.events, e) }

// fakeBackend emits one text event then returns a fixed reply.
type fakeBackend struct{ reply string }

func (b fakeBackend) Respond(_ context.Context, _ contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	if onEvent != nil {
		onEvent(contracts.BackendEvent{Kind: "text", Detail: "thinking"})
	}
	return b.reply, nil
}
func (fakeBackend) Close() error { return nil }

// fakeGateway records posts/replies; the other methods are no-ops.
type fakeGateway struct{ posts []string }

func (fakeGateway) Manifest() contracts.Manifest { return contracts.Manifest{} }
func (g *fakeGateway) Post(_ context.Context, _ contracts.Conversation, text string) (contracts.MessageID, error) {
	g.posts = append(g.posts, text)
	return "1", nil
}
func (g *fakeGateway) Reply(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	g.posts = append(g.posts, text)
	return "1", nil
}
func (*fakeGateway) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (*fakeGateway) Menu(context.Context, contracts.Conversation, contracts.MessageID, string, []contracts.Choice) error {
	return nil
}

// fakeReader is a ChannelReader for handle tests: UpsertStatusMessage is
// unreached when Progress is off; Unreact/Read are simple no-ops.
type fakeReader struct{}

func (fakeReader) Enabled() bool          { return true }
func (fakeReader) DefaultChannel() string { return "c1" }
func (fakeReader) EnsureChannel(context.Context, string, string) (contracts.Channel, error) {
	return contracts.Channel{}, nil
}
func (fakeReader) Read(context.Context, string, int, string) ([]contracts.Message, error) {
	return nil, nil
}
func (fakeReader) Unreact(context.Context, string, string, string) error { return nil }
func (fakeReader) UpsertStatusMessage(context.Context, string, string, string) (string, error) {
	return "", nil
}

func TestHandleEmitsTurnEvents(t *testing.T) {
	gw := &fakeGateway{}
	sink := &fakeSink{}
	r := &runner{
		p:    fakeReader{},
		gw:   gw,
		resp: fakeBackend{reply: "done · 4 files changed"},
		conv: contracts.Conversation{Gateway: "discord", ID: "c1"},
		ch:   "c1",
		o:    Options{Progress: "off"},
		seen: map[string]bool{},
		sink: sink,
	}

	r.handle(context.Background(), contracts.Message{
		ID:         "m1",
		ChannelID:  "c1",
		Content:    "refactor the env loader",
		AuthorID:   "u1",
		AuthorName: "alice",
	})

	if len(gw.posts) != 1 || gw.posts[0] != "done · 4 files changed" {
		t.Fatalf("gateway posts = %v, want one reply", gw.posts)
	}
	want := []contracts.Event{
		{T: "human", Who: "alice", Text: "refactor the env loader"},
		{T: "chunk", Text: "thinking"},
		{T: "reply", Text: "done · 4 files changed", Done: true},
	}
	if len(sink.events) != len(want) {
		t.Fatalf("emitted %d events, want %d: %+v", len(sink.events), len(want), sink.events)
	}
	for i := range want {
		if sink.events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, sink.events[i], want[i])
		}
	}
}

func TestEmitBackendMapping(t *testing.T) {
	cases := []struct {
		name string
		ev   contracts.BackendEvent
		want []contracts.Event
	}{
		{"tool", contracts.BackendEvent{Kind: "tool", Tool: "Edit", Detail: "file.go"}, []contracts.Event{{T: "status", Text: "Edit file.go"}}},
		{"reset", contracts.BackendEvent{Kind: "reset"}, []contracts.Event{{T: "reset"}}},
		{"result drops", contracts.BackendEvent{Kind: "result", Detail: "ignored"}, nil},
		{"empty tool drops", contracts.BackendEvent{Kind: "tool"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &fakeSink{}
			(&runner{sink: sink}).emitBackend(c.ev)
			if len(sink.events) != len(c.want) {
				t.Fatalf("emitted %+v, want %+v", sink.events, c.want)
			}
			for i := range c.want {
				if sink.events[i] != c.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, sink.events[i], c.want[i])
				}
			}
		})
	}
}

func TestHandleNilSinkNoPanic(t *testing.T) {
	gw := &fakeGateway{}
	r := &runner{
		p:    fakeReader{},
		gw:   gw,
		resp: fakeBackend{reply: "ok"},
		conv: contracts.Conversation{Gateway: "discord", ID: "c1"},
		ch:   "c1",
		o:    Options{Progress: "off"},
		seen: map[string]bool{},
		sink: nil,
	}
	r.handle(context.Background(), contracts.Message{ID: "m1", ChannelID: "c1", Content: "hi", AuthorID: "u1", AuthorName: "bob"})
	if len(gw.posts) != 1 {
		t.Fatalf("gateway posts = %v, want one reply even with nil sink", gw.posts)
	}
}
