package terminal

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/plugins/terminal/tui"
)

// The gateway the factory builds must expose the Foreground capability, since
// serve discovers the bound gateway to run on the main thread through that
// interface (not a concrete import). Guards against silently dropping it.
func TestGatewaySetExposesForeground(t *testing.T) {
	set, err := newGatewaySet(context.Background(), contracts.PluginConfig{})
	if err != nil {
		t.Fatalf("newGatewaySet: %v", err)
	}
	if _, ok := set.Gateway.(contracts.Foreground); !ok {
		t.Fatal("terminal gateway must implement contracts.Foreground")
	}
}

func TestReadDrainsPerChannel(t *testing.T) {
	tm := New()
	tm.Submit("chA", "hello")
	tm.Submit("chB", "world")

	a, _ := tm.Read(context.Background(), "chA", 100, "")
	if len(a) != 1 || a[0].Content != "hello" || a[0].ChannelID != "chA" {
		t.Fatalf("chA Read = %+v", a)
	}
	b, _ := tm.Read(context.Background(), "chB", 100, "")
	if len(b) != 1 || b[0].Content != "world" {
		t.Fatalf("chB Read = %+v", b)
	}
	// drained
	if a2, _ := tm.Read(context.Background(), "chA", 100, ""); len(a2) != 0 {
		t.Fatalf("chA second Read = %+v, want empty", a2)
	}
}

func TestEmitToRoutesToFrontend(t *testing.T) {
	tm := New()
	got := make(chan tui.RoutedEvent, 1)
	go func() { got <- <-tm.Frontend() }()
	tm.EmitTo(contracts.Conversation{Gateway: "terminal", ID: "chX"}, contracts.Event{T: "chunk", Text: "a"})
	re := <-got
	if re.Conv.ID != "chX" || re.Event.Text != "a" {
		t.Fatalf("frontend got %+v", re)
	}
}

func TestEmitUsesDefaultChannel(t *testing.T) {
	tm := New()
	got := make(chan tui.RoutedEvent, 1)
	go func() { got <- <-tm.Frontend() }()
	tm.Emit(contracts.Event{T: "reply", Text: "b", Done: true})
	re := <-got
	if re.Conv.ID != ChannelID || re.Event.Text != "b" {
		t.Fatalf("Emit default-channel routing wrong: %+v", re)
	}
}

func TestTerminalImplementsRoutedEventSink(t *testing.T) {
	var _ contracts.RoutedEventSink = New()
}

func TestPostEmitsReplyEvent(t *testing.T) {
	tm := New()
	got := make(chan tui.RoutedEvent, 1)
	go func() { got <- <-tm.Frontend() }()
	if _, err := tm.Post(context.Background(), contracts.Conversation{Gateway: "terminal", ID: "terminal"}, "hi"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if re := <-got; re.Event.T != "reply" {
		t.Fatalf("Post emitted %+v, want reply", re)
	}
}
