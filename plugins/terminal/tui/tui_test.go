package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	contracts "github.com/Herrscherd/herrscher-contracts"
)

func newTestModel() *model { return newModel(nil) }

func TestRoutedEventLandsInOwnTab(t *testing.T) {
	m := newTestModel()
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "chunk", Text: "hello-a"}})
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "b"}, Event: contracts.Event{T: "chunk", Text: "hello-b"}})

	if got := strings.Join(m.tabs["a"].lines, "\n"); !strings.Contains(got, "hello-a") {
		t.Fatalf("tab a missing its line: %q", got)
	}
	if got := strings.Join(m.tabs["b"].lines, "\n"); strings.Contains(got, "hello-a") {
		t.Fatalf("tab b leaked tab a's line: %q", got)
	}
}

func TestUnreadSetOnInactiveTab(t *testing.T) {
	m := newTestModel()
	m.ensureTab("a") // first tab becomes active
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "b"}, Event: contracts.Event{T: "chunk", Text: "x"}})
	if !m.tabs["b"].unread {
		t.Fatal("event on inactive tab b must mark it unread")
	}
	if m.tabs["a"].unread {
		t.Fatal("active tab a must not be unread")
	}
}

func TestSwitchTabClearsUnread(t *testing.T) {
	m := newTestModel()
	m.ensureTab("a")
	m.ensureTab("b")
	m.tabs["b"].unread = true
	m.active = "a"
	m.switchTab(1) // move to next tab -> b
	if m.active != "b" {
		t.Fatalf("active = %q, want b", m.active)
	}
	if m.tabs["b"].unread {
		t.Fatal("switching to b must clear its unread")
	}
}

func TestRenderEventShowsCostPerTab(t *testing.T) {
	m := newTestModel()
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "reply", Text: "done", Done: true, Cost: 0.0042}})
	joined := strings.Join(m.tabs["a"].lines, "\n")
	if !strings.Contains(joined, "done") || !strings.Contains(joined, "$0.0042") {
		t.Fatalf("cost/reply dropped: %q", joined)
	}
}

func TestRenderEventOmitsZeroCost(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "reply", Text: "done", Done: true}})
	for _, l := range m.tabs["a"].lines {
		if strings.Contains(l, "$") {
			t.Fatalf("zero-cost turn must not show a cost line; got %q", l)
		}
	}
}

func TestRenderEventMarksAbandonedPerTab(t *testing.T) {
	m := newTestModel()
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "abandoned"}})
	if !strings.Contains(strings.Join(m.tabs["a"].lines, "\n"), "abandoned") {
		t.Fatal("abandoned not surfaced")
	}
}

// fakeBackend satisfies the Backend interface for unit tests.
type fakeBackend struct {
	dispatched [][]string
	sessions   []contracts.SessionInfo
	fe         chan RoutedEvent
}

func (f *fakeBackend) Frontend() <-chan RoutedEvent        { return f.fe }
func (f *fakeBackend) Submit(string, string)               {}
func (f *fakeBackend) Sessions() []contracts.SessionInfo   { return f.sessions }
func (f *fakeBackend) Dispatch(args []string) (string, error) {
	f.dispatched = append(f.dispatched, args)
	return "ok", nil
}

func TestSlashLineDispatches(t *testing.T) {
	f := &fakeBackend{}
	m := newModel(f)
	m.ensureTab("a")
	m.input.SetValue("/session list")
	m.handleEnter()
	if len(f.dispatched) != 1 || f.dispatched[0][0] != "session" {
		t.Fatalf("slash line not dispatched: %+v", f.dispatched)
	}
}

func TestPlainLineSubmits(t *testing.T) {
	f := &fakeBackend{}
	m := newModel(f)
	m.ensureTab("a")
	m.input.SetValue("hello world")
	m.handleEnter()
	if len(f.dispatched) != 0 {
		t.Fatalf("plain line must not dispatch: %+v", f.dispatched)
	}
}

func TestClosedEventRemovesTab(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.ensureTab("a")
	m.ensureTab("b")
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "b"}, Event: contracts.Event{T: "closed"}})
	if _, ok := m.tabs["b"]; ok {
		t.Fatal("closed event must remove tab b")
	}
	for _, ch := range m.order {
		if ch == "b" {
			t.Fatal("closed tab still in order")
		}
	}
}

func TestSyncTabsFromSessions(t *testing.T) {
	f := &fakeBackend{sessions: []contracts.SessionInfo{{Name: "alpha", ChannelID: "terminal/alpha-1"}}}
	m := newModel(f)
	m.syncTabs()
	tb, ok := m.tabs["terminal/alpha-1"]
	if !ok || tb.label != "alpha" {
		t.Fatalf("tab not synced/labelled from Sessions(): %+v", m.tabs)
	}
}

func TestTabBusyLifecycle(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "status", Text: "working"}})
	if !m.tabs["a"].busy {
		t.Fatal("status must mark tab busy")
	}
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "reply", Text: "done", Done: true, Cost: 0.01}})
	if m.tabs["a"].busy {
		t.Fatal("done reply must clear busy")
	}
	if m.tabs["a"].lastCost != 0.01 {
		t.Fatalf("lastCost = %v", m.tabs["a"].lastCost)
	}
}

func TestResizeSyncsViewport(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.ensureTab("a")
	m.tabs["a"].lines = []string{"line1", "line2", "line3"}
	// Simulate init WindowSizeMsg
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Simulate resize WindowSizeMsg (the else branch)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.vp.Width != 100 || m.vp.Height != 25 {
		t.Fatalf("resize: vp.Width=%d (want 100), vp.Height=%d (want 25)", m.vp.Width, m.vp.Height)
	}
}

func TestHelpToggle(t *testing.T) {
	m := newModel(&fakeBackend{})
	if m.showHelp {
		t.Fatal("help off by default")
	}
	m.toggleHelp()
	if !m.showHelp {
		t.Fatal("help must toggle on")
	}
}

func TestCloseActiveDispatchesClose(t *testing.T) {
	f := &fakeBackend{}
	m := newModel(f)
	tb := m.ensureTab("terminal/alpha-1")
	tb.label = "alpha"
	m.active = "terminal/alpha-1"
	m.confirmClose() // simulate confirmed close
	if len(f.dispatched) != 1 {
		t.Fatalf("close not dispatched: %+v", f.dispatched)
	}
	// Assert positional args: session close --name alpha
	if len(f.dispatched[0]) != 4 || f.dispatched[0][0] != "session" || f.dispatched[0][1] != "close" || f.dispatched[0][2] != "--name" || f.dispatched[0][3] != "alpha" {
		t.Fatalf("close args mismatch: got %+v, want [session close --name alpha]", f.dispatched[0])
	}
}

func TestAbandonedSetsDisconnected(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "abandoned"}})
	if !m.tabs["a"].disconnected {
		t.Fatal("abandoned must set disconnected")
	}
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "chunk", Text: "hi"}})
	if m.tabs["a"].disconnected {
		t.Fatal("chunk must clear disconnected")
	}
}

func TestQuestionMarkTogglesHelpWhenEmpty(t *testing.T) {
	m := newModel(&fakeBackend{})
	if m.showHelp {
		t.Fatal("help must start off")
	}
	// Send ? with empty input
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	m.Update(msg)
	if !m.showHelp {
		t.Fatal("? with empty input must toggle help on")
	}
	// Send ? again to toggle off
	m.Update(msg)
	if m.showHelp {
		t.Fatal("? with empty input must toggle help off")
	}
}

func TestQuestionMarkTypedWhenInputNonEmpty(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.input.SetValue("foo")
	if m.showHelp {
		t.Fatal("help must start off")
	}
	// Send ? with non-empty input
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	m.Update(msg)
	if m.showHelp {
		t.Fatal("? with non-empty input must not toggle help")
	}
	// Verify ? was typed into input (should have "foo?")
	if !strings.Contains(m.input.Value(), "?") {
		t.Fatalf("? must be typed into input: got %q", m.input.Value())
	}
}

func TestPendingCloseCtrlCQuits(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.pendingClose = true
	if !m.pendingClose {
		t.Fatal("pendingClose must be true")
	}
	// Send Ctrl+C while pendingClose is true
	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd := m.Update(msg)
	if m.pendingClose {
		t.Fatal("Ctrl+C during pendingClose must clear pendingClose")
	}
	// tea.Quit() is a function that returns a Cmd; we verify cmd is not nil (it's the quit command)
	if cmd == nil {
		t.Fatal("Ctrl+C during pendingClose must return a quit command")
	}
}

func TestPendingCloseEscQuits(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.pendingClose = true
	// Send Esc while pendingClose is true
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := m.Update(msg)
	if m.pendingClose {
		t.Fatal("Esc during pendingClose must clear pendingClose")
	}
	// tea.Quit() is a function that returns a Cmd; we verify cmd is not nil
	if cmd == nil {
		t.Fatal("Esc during pendingClose must return a quit command")
	}
}

func TestPendingCloseOtherKeyCancels(t *testing.T) {
	m := newModel(&fakeBackend{})
	m.pendingClose = true
	// Send 'n' (any non-y key) while pendingClose is true
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	_, cmd := m.Update(msg)
	if m.pendingClose {
		t.Fatal("non-y key during pendingClose must clear pendingClose")
	}
	if cmd != nil {
		t.Fatalf("non-y key during pendingClose must return nil cmd, got %v", cmd)
	}
}
