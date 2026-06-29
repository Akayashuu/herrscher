package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestMenuRendersChoices(t *testing.T) {
	tm := New()
	conv := contracts.Conversation{Gateway: "terminal", ID: "ch"}
	if err := tm.Menu(context.Background(), conv, "", "pick one", []contracts.Choice{
		{Label: "first", Value: "1"},
		{Label: "second", Value: "2"},
	}); err != nil {
		t.Fatal(err)
	}
	re := <-tm.Frontend()
	if !strings.Contains(re.Event.Text, "first") || !strings.Contains(re.Event.Text, "second") {
		t.Fatalf("menu must render its choices, got %q", re.Event.Text)
	}
}

func TestEmitDeliversControlEventUnderBackpressure(t *testing.T) {
	tm := New()
	// Fill the outbound buffer with droppable chunk events.
	for i := 0; i < cap(tm.out); i++ {
		tm.emit(tui.RoutedEvent{Event: contracts.Event{T: "chunk", Text: "x"}})
	}
	// A finished reply must still be delivered (not dropped); drain one slot in
	// parallel so the brief wait succeeds.
	go func() { <-tm.Frontend() }()
	done := make(chan struct{})
	go func() {
		tm.emit(tui.RoutedEvent{Event: contracts.Event{T: "reply", Done: true, Text: "final"}})
		close(done)
	}()
	<-done
}

func TestBootstrapWaitsForBindThenCreates(t *testing.T) {
	tm := New()
	fake := &fakeSessionControl{}
	done := make(chan struct{})
	go func() {
		tm.bootstrapDefaultSession(context.Background())
		close(done)
	}()
	// Bind after a beat: the ready signal must wake the bootstrap immediately.
	tm.BindSessionControl(fake)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not return after bind")
	}
	if len(fake.lastArgs) < 2 || fake.lastArgs[1] != "create" {
		t.Fatalf("bootstrap did not create a default session: %v", fake.lastArgs)
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
	re := <-got
	if re.Event.T != "reply" {
		t.Fatalf("Post emitted %+v, want reply", re)
	}
	if re.Event.Text != "hi" {
		t.Fatalf("Post reply text = %q, want %q", re.Event.Text, "hi")
	}
}

func TestTerminalImplementsChannelAdmin(t *testing.T) {
	var _ contracts.ChannelAdmin = New()
}

func TestCreateUnderMintsUniqueChannels(t *testing.T) {
	tm := New()
	a, err := tm.CreateUnder(context.Background(), "home", "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := tm.CreateUnder(context.Background(), "home", "Alpha")
	if a == b {
		t.Fatalf("CreateUnder must mint unique ids, got %q twice", a)
	}
	if !strings.HasPrefix(a, "terminal/") {
		t.Fatalf("channel id %q must be terminal-namespaced", a)
	}
}

func TestArchiveEmitsCloseToTab(t *testing.T) {
	tm := New()
	got := make(chan tui.RoutedEvent, 1)
	go func() { got <- <-tm.Frontend() }()
	_ = tm.Archive(context.Background(), "terminal/x")
	re := <-got
	if re.Conv.ID != "terminal/x" || re.Event.T != "closed" {
		t.Fatalf("Archive must emit a 'closed' event to the tab: %+v", re)
	}
}

func TestGatewaySetExposesAdmin(t *testing.T) {
	set, _ := newGatewaySet(context.Background(), contracts.PluginConfig{})
	if set.Admin == nil {
		t.Fatal("terminal GatewaySet must expose ChannelAdmin")
	}
}

// fakeSessionControl is a minimal contracts.SessionControl for Dispatch and
// ensureDefaultSession tests.
type fakeSessionControl struct {
	lastArgs []string
	sessions []contracts.SessionInfo
}

func (f *fakeSessionControl) Dispatch(_ context.Context, args []string) (string, error) {
	f.lastArgs = args
	return "ok", nil
}

func (f *fakeSessionControl) Sessions() []contracts.SessionInfo { return f.sessions }

func TestDispatchDefaultsSessionCreateToTerminal(t *testing.T) {
	tm := New()
	fake := &fakeSessionControl{}
	tm.BindSessionControl(fake)
	if _, err := tm.Dispatch([]string{"session", "create", "--name", "x"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range fake.lastArgs {
		if a == "--terminal_only" {
			return
		}
	}
	t.Fatalf("--terminal_only not appended: %v", fake.lastArgs)
}

func TestDispatchRespectsExplicitGateways(t *testing.T) {
	tm := New()
	fake := &fakeSessionControl{}
	tm.BindSessionControl(fake)
	if _, err := tm.Dispatch([]string{"session", "create", "--name", "x", "--gateways", "discord"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range fake.lastArgs {
		if a == "--terminal_only" {
			t.Fatalf("--terminal_only must NOT be appended when --gateways given: %v", fake.lastArgs)
		}
	}
}

func TestDispatchPassesThroughNonCreate(t *testing.T) {
	tm := New()
	fake := &fakeSessionControl{}
	tm.BindSessionControl(fake)
	if _, err := tm.Dispatch([]string{"session", "list"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.lastArgs) != 2 || fake.lastArgs[0] != "session" || fake.lastArgs[1] != "list" {
		t.Fatalf("args changed for non-create: %v", fake.lastArgs)
	}
}

func TestDispatchRejectsNonSessionVerbs(t *testing.T) {
	// The TUI forwards any /-verb to Dispatch; gate the seam so daemon-management
	// verbs (which could restart the host the TUI runs in, or rewrite its config)
	// are never reachable from the terminal. Only session-scoped verbs pass.
	for _, argv := range [][]string{
		{"service", "restart"},
		{"service", "update"},
		{"set", "home", "--id", "x"},
	} {
		tm := New()
		fake := &fakeSessionControl{}
		tm.BindSessionControl(fake)
		if _, err := tm.Dispatch(argv); err == nil {
			t.Fatalf("Dispatch(%v) should be rejected from the terminal", argv)
		}
		if fake.lastArgs != nil {
			t.Fatalf("rejected verb must not reach SessionControl; got args: %v", fake.lastArgs)
		}
	}
}

func TestDispatchAllowsSessionAndAgentVerbs(t *testing.T) {
	for _, argv := range [][]string{
		{"session", "list"},
		{"session", "close", "--name", "x"},
		{"agent", "list"},
	} {
		tm := New()
		fake := &fakeSessionControl{}
		tm.BindSessionControl(fake)
		if _, err := tm.Dispatch(argv); err != nil {
			t.Fatalf("Dispatch(%v) should be allowed: %v", argv, err)
		}
		if fake.lastArgs == nil {
			t.Fatalf("allowed verb %v must reach SessionControl", argv)
		}
	}
}

// --- ensureDefaultSession ---

func TestEnsureDefaultSessionCreatesWhenNone(t *testing.T) {
	fake := &fakeSessionControl{} // Sessions() returns nil/empty
	if err := ensureDefaultSession(context.Background(), fake); err != nil {
		t.Fatalf("ensureDefaultSession: %v", err)
	}
	if len(fake.lastArgs) < 2 || fake.lastArgs[0] != "session" || fake.lastArgs[1] != "create" {
		t.Fatalf("expected session create, got: %v", fake.lastArgs)
	}
	hasTerminalOnly, hasName := false, false
	for _, a := range fake.lastArgs {
		if a == "--terminal_only" {
			hasTerminalOnly = true
		}
		if a == "main" {
			hasName = true
		}
	}
	if !hasTerminalOnly {
		t.Fatalf("--terminal_only missing from args: %v", fake.lastArgs)
	}
	if !hasName {
		t.Fatalf("--name main missing from args: %v", fake.lastArgs)
	}
}

func TestEnsureDefaultSessionSkipsWhenTerminalExists(t *testing.T) {
	fake := &fakeSessionControl{
		sessions: []contracts.SessionInfo{
			{Name: "main", ChannelID: "ch1", Type: "shared", Gateways: []string{"terminal"}},
		},
	}
	if err := ensureDefaultSession(context.Background(), fake); err != nil {
		t.Fatalf("ensureDefaultSession: %v", err)
	}
	if fake.lastArgs != nil {
		t.Fatalf("Dispatch must not be called when a terminal session exists; got args: %v", fake.lastArgs)
	}
}

func TestEnsureDefaultSessionCreatesWhenOnlyDiscord(t *testing.T) {
	fake := &fakeSessionControl{
		sessions: []contracts.SessionInfo{
			{Name: "discord-main", ChannelID: "ch2", Type: "shared", Gateways: []string{"discord"}},
		},
	}
	if err := ensureDefaultSession(context.Background(), fake); err != nil {
		t.Fatalf("ensureDefaultSession: %v", err)
	}
	if len(fake.lastArgs) < 2 || fake.lastArgs[0] != "session" || fake.lastArgs[1] != "create" {
		t.Fatalf("expected session create when only discord session exists; got: %v", fake.lastArgs)
	}
}
