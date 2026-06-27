# Terminal TUI Multi-Session Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one local terminal drive multiple concurrent agent sessions as tabs (one visible, others streaming in the background), created and managed with slash commands in the TUI, with no Discord required.

**Architecture:** Add an optional `RoutedEventSink` capability to `herrscher-contracts` so the host tags each fanned-out event with the session's `Conversation`; the shared terminal gateway becomes a real multi-channel gateway (per-channel input queues, `EmitTo` demux, synthetic `ChannelAdmin` channels, `SessionControlReceiver`); the Bubbletea TUI demultiplexes routed events into per-tab transcripts. The host stays gateway-agnostic (it only learns `Conversation`), the TUI never imports the terminal plugin, and the session manager is generalized off Discord.

**Tech Stack:** Go 1.25, Bubbletea/Bubbles/Lipgloss (charmbracelet), `go.work` workspace (`herrscher`, `herrscher-contracts`, `herrscher-orchestrator`, `herrscher-transport`).

## Global Constraints

- Go module path: `github.com/Herrscherd/herrscher`; contracts import alias is `contracts "github.com/Herrscherd/herrscher-contracts"`.
- `herrscher-contracts` is edited in its workspace checkout at `C:\Users\Kara\Desktop\Code\herrscher-contracts` (used live via `go.work`); no version bump needed during development.
- The host/core must import **zero** platform-specific code — enforced by `purity_test.go`. New code in `core/` may reference only `contracts` types, never the terminal/discord plugins.
- `contracts.Event`'s JSON wire shape must NOT change (it is the bridge↔hub line protocol). Routing is carried out-of-band via the `RoutedEventSink` method parameter, not new Event fields.
- TDD: every task writes a failing test first, watches it fail, implements minimally, watches it pass, commits.
- Run Go tests from the repo root `C:\Users\Kara\Desktop\Code\herrscher` with `go test ./<pkg>/...`. Contracts tests run from `C:\Users\Kara\Desktop\Code\herrscher-contracts` with `go test ./...`.
- Existing terminal single-session behavior must keep working (the fixed `"terminal"` channel becomes the default/legacy tab).
- Commit messages end with: `Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68`.

---

## Phase 1 — Routing plumbing + multi-tab rendering

End state: a session bound to the terminal gateway (created via the existing CLI, e.g. `herrscher session create --name foo --gateways terminal`) appears as a live tab whose `chunk`/`status`/`reply` events stream into the correct pane; `Tab`/`Shift+Tab` switch tabs; the single-session path still works.

### Task 1: `RoutedEventSink` capability + `Degrade` passthrough (contracts)

**Files:**
- Modify: `C:\Users\Kara\Desktop\Code\herrscher-contracts\event.go`
- Modify: `C:\Users\Kara\Desktop\Code\herrscher-contracts\degrade.go`
- Test: `C:\Users\Kara\Desktop\Code\herrscher-contracts\degrade_test.go` (new)

**Interfaces:**
- Produces: `contracts.RoutedEventSink interface { EmitTo(conv Conversation, e Event) }`.
- Produces: `Degrade(g)` returns a wrapper that satisfies `EventSink` and/or `RoutedEventSink` iff the wrapped `g` does, forwarding `Emit`/`EmitTo`.

- [ ] **Step 1: Write the failing test**

Create `C:\Users\Kara\Desktop\Code\herrscher-contracts\degrade_test.go`. `Degrade` will declare `Emit`/`EmitTo` unconditionally (so the wrapper always satisfies both interfaces); the meaningful behavior is that calls are forwarded to the inner gateway when it implements the matching sink. The test asserts that forwarding:

```go
package contracts

import (
	"context"
	"testing"
)

// recordingSink is a Gateway that also records routed/plain sink calls.
type recordingSink struct {
	plain  []Event
	routed []Conversation
}

func (r *recordingSink) Manifest() Manifest { return Manifest{Kind: "rec"} }
func (r *recordingSink) Post(context.Context, Conversation, string) (MessageID, error) {
	return "", nil
}
func (r *recordingSink) Reply(context.Context, Conversation, MessageID, string) (MessageID, error) {
	return "", nil
}
func (r *recordingSink) React(context.Context, Conversation, MessageID, string) error { return nil }
func (r *recordingSink) Menu(context.Context, Conversation, MessageID, string, []Choice) error {
	return nil
}
func (r *recordingSink) Emit(e Event)                   { r.plain = append(r.plain, e) }
func (r *recordingSink) EmitTo(c Conversation, _ Event) { r.routed = append(r.routed, c) }

func TestDegradeForwardsSinks(t *testing.T) {
	rec := &recordingSink{}
	d := Degrade(rec)

	es, ok := d.(EventSink)
	if !ok {
		t.Fatal("degraded gateway must satisfy EventSink")
	}
	es.Emit(Event{T: "chunk", Text: "x"})
	if len(rec.plain) != 1 {
		t.Fatalf("Emit not forwarded to inner: %+v", rec.plain)
	}

	rs, ok := d.(RoutedEventSink)
	if !ok {
		t.Fatal("degraded gateway must satisfy RoutedEventSink")
	}
	rs.EmitTo(Conversation{Gateway: "rec", ID: "c1"}, Event{T: "reply"})
	if len(rec.routed) != 1 || rec.routed[0].ID != "c1" {
		t.Fatalf("EmitTo not forwarded to inner: %+v", rec.routed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run (from `C:\Users\Kara\Desktop\Code\herrscher-contracts`): `go test ./... -run TestDegradeForwardsSinks -v`
Expected: COMPILE FAIL — `RoutedEventSink` undefined and `degrading` does not implement `EventSink`/`RoutedEventSink`.

- [ ] **Step 3: Add `RoutedEventSink` to `event.go`**

Append to `event.go`:

```go
// RoutedEventSink is an optional gateway capability for a gateway that renders
// more than one conversation's live stream itself (the multi-session terminal
// TUI). When a gateway implements it the hub prefers it over EventSink and tags
// each event with the destination Conversation, so the gateway can demultiplex
// the streams of every session bound to it. A gateway that implements only
// EventSink (or neither) is unaffected.
type RoutedEventSink interface {
	EmitTo(conv Conversation, e Event)
}
```

- [ ] **Step 4: Forward the sinks in `degrade.go`**

Add these methods to `degrade.go` (the `degrading` type). `degrading` declares them unconditionally — so `Degrade(g)` always satisfies both interfaces — and forwards to the inner gateway only when the inner implements the matching sink (a no-op otherwise; harmless because the host calls these only for gateways that are sinks):

```go
func (d degrading) Emit(e Event) {
	if s, ok := d.g.(EventSink); ok {
		s.Emit(e)
	}
}

func (d degrading) EmitTo(conv Conversation, e Event) {
	if s, ok := d.g.(RoutedEventSink); ok {
		s.EmitTo(conv, e)
		return
	}
	if s, ok := d.g.(EventSink); ok {
		s.Emit(e) // inner renders unrouted; acceptable for single-conversation gateways
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./... -run TestDegradeForwardsSinks -v`
Expected: PASS.

- [ ] **Step 6: Run the full contracts suite**

Run: `go test ./...`
Expected: PASS (no regressions).

- [ ] **Step 7: Commit**

```bash
git add event.go degrade.go degrade_test.go
git commit -m "feat(contracts): RoutedEventSink + Degrade sink passthrough

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 2: `fanOut` prefers `RoutedEventSink` (host)

**Files:**
- Modify: `core/host/turnloop.go:260-274` (`fanOut`)
- Test: `core/host/turnloop_test.go` (add a test)

**Interfaces:**
- Consumes: `contracts.RoutedEventSink`, `contracts.Conversation` (Task 1).
- Produces: `fanOut` calls `sink.EmitTo(Conversation{Gateway: kind, ID: d.renderChannel(g)}, e)` when the gateway is a `RoutedEventSink`, else falls back to `EventSink.Emit`, else the renderer.

- [ ] **Step 1: Write the failing test**

Add to `core/host/turnloop_test.go`:

```go
// A gateway implementing RoutedEventSink must receive EmitTo with the session's
// conversation (gateway kind + bound channel), in preference to Emit.
func TestFanOutPrefersRoutedEventSink(t *testing.T) {
	rec := &routedRec{}
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: rec}}, nil, nil)
	d.channel = "chan-1"

	d.fanOut(context.Background(), contracts.Event{T: "chunk", Text: "hi"})

	if len(rec.convs) != 1 {
		t.Fatalf("EmitTo not called once: %+v", rec.convs)
	}
	if rec.convs[0].ID != "chan-1" || rec.convs[0].Gateway != "term" {
		t.Fatalf("wrong conversation: %+v", rec.convs[0])
	}
	if rec.plainCalls != 0 {
		t.Fatalf("Emit should not be called when RoutedEventSink is present")
	}
}

// routedRec is a minimal Gateway + RoutedEventSink + EventSink recorder.
type routedRec struct {
	convs      []contracts.Conversation
	plainCalls int
}

func (r *routedRec) Manifest() contracts.Manifest { return contracts.Manifest{Kind: "term"} }
func (r *routedRec) Post(context.Context, contracts.Conversation, string) (contracts.MessageID, error) {
	return "", nil
}
func (r *routedRec) Reply(context.Context, contracts.Conversation, contracts.MessageID, string) (contracts.MessageID, error) {
	return "", nil
}
func (r *routedRec) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (r *routedRec) Menu(context.Context, contracts.Conversation, contracts.MessageID, string, []contracts.Choice) error {
	return nil
}
func (r *routedRec) Emit(contracts.Event) { r.plainCalls++ }
func (r *routedRec) EmitTo(c contracts.Conversation, _ contracts.Event) {
	r.convs = append(r.convs, c)
}
```

Ensure the test file imports `"context"` and `contracts "github.com/Herrscherd/herrscher-contracts"` (likely already present).

- [ ] **Step 2: Run test to verify it fails**

Run (from repo root): `go test ./core/host/ -run TestFanOutPrefersRoutedEventSink -v`
Expected: FAIL — `EmitTo` not called (currently `Emit` path taken), so `len(rec.convs) == 0`.

- [ ] **Step 3: Update `fanOut`**

Replace the body of `fanOut` (`core/host/turnloop.go:260`) so the routed sink wins:

```go
func (d *sessionDriver) fanOut(ctx context.Context, e contracts.Event) {
	for i, g := range d.gateways {
		if rs, ok := g.Gateway.(contracts.RoutedEventSink); ok {
			rs.EmitTo(contracts.Conversation{
				Gateway: g.Gateway.Manifest().Kind,
				ID:      d.renderChannel(g),
			}, e)
			continue
		}
		if sink, ok := g.Gateway.(contracts.EventSink); ok {
			sink.Emit(e)
			continue
		}
		key := strconv.Itoa(i) + ":" + g.Gateway.Manifest().Kind
		r := d.renderers[key]
		if r == nil {
			r = newGatewayRenderer(g.Gateway, d.renderChannel(g))
			d.renderers[key] = r
		}
		r.handle(ctx, e)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core/host/ -run TestFanOutPrefersRoutedEventSink -v`
Expected: PASS.

- [ ] **Step 5: Run the host suite**

Run: `go test ./core/host/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add core/host/turnloop.go core/host/turnloop_test.go
git commit -m "feat(host): fanOut prefers RoutedEventSink, tags events with conversation

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 3: Terminal gateway becomes multi-channel (`EmitTo`, per-channel queues, routed Frontend)

**Files:**
- Modify: `plugins/terminal/terminal.go`
- Modify: `plugins/terminal/tui/tui.go` (add `RoutedEvent` + `Backend` seam change)
- Test: `plugins/terminal/terminal_test.go`

**Interfaces:**
- Produces (tui pkg): `type RoutedEvent struct { Conv contracts.Conversation; Event contracts.Event }`.
- Produces (tui pkg): `Backend interface { Frontend() <-chan RoutedEvent; Submit(channel, text string); Sessions() []contracts.SessionInfo }`.
- Produces (terminal): `Terminal` implements `contracts.RoutedEventSink` (`EmitTo`) and keeps `contracts.EventSink` (`Emit` delegates to `EmitTo` with the default `ChannelID`). Per-channel inbound queues keyed by channel id. `Submit(channel, text)` enqueues under that channel.
- Consumes: `contracts.RoutedEventSink`, `contracts.SessionInfo` (Tasks 1 + existing contracts).

- [ ] **Step 1: Write the failing tests**

Replace the body of `plugins/terminal/terminal_test.go`'s `TestReadDrainsSubmittedLines`, `TestEmitForwardsToFrontend`, `TestPostEmitsReplyEvent` and add new ones:

```go
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
```

Add `"github.com/Herrscherd/herrscher/plugins/terminal/tui"` to the test imports. Keep `TestGatewaySetExposesForeground`. Update `TestPostEmitsReplyEvent` to read a `RoutedEvent` and assert `re.Event.T == "reply"`.

Also add an interface-satisfaction guard test:

```go
func TestTerminalImplementsRoutedEventSink(t *testing.T) {
	var _ contracts.RoutedEventSink = New()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./plugins/terminal/ -v`
Expected: COMPILE FAIL — `tui.RoutedEvent` undefined, `Submit` arity changed, `EmitTo` missing.

- [ ] **Step 3: Add the routed seam to `tui/tui.go`**

In `plugins/terminal/tui/tui.go`, replace the `Backend` interface and add `RoutedEvent`:

```go
// RoutedEvent is a turn event tagged with the conversation (session channel) it
// belongs to, so the TUI can route it to the right tab.
type RoutedEvent struct {
	Conv  contracts.Conversation
	Event contracts.Event
}

// Backend is the narrow view of the terminal gateway the TUI drives: it reads
// routed outbound events to render, submits the lines the operator types into a
// specific channel, and enumerates the hub's sessions for tab labels. Taking an
// interface keeps this package free of any dependency on the terminal plugin.
type Backend interface {
	Frontend() <-chan RoutedEvent
	Submit(channel, text string)
	Sessions() []contracts.SessionInfo
}
```

(The full TUI consumer rewrite is Task 4; for now the seam just needs to compile. Temporarily, the existing `model` will not build against the new `Backend` — wrap the model changes into Task 4 and, to keep Task 3 green, make the minimal `tui.go` edits: change `eventMsg` forwarding to send `RoutedEvent` and the model to ignore `Conv` for now. Simplest: in `Run`, forward `eventMsg(e.Event)` so the existing single-tab model still compiles. Keep `model` unchanged in Task 3.)

Concretely, in `Run`'s forward goroutine change:

```go
case e, ok := <-tm.Frontend():
	if !ok {
		return
	}
	p.Send(eventMsg(e.Event))
```

- [ ] **Step 4: Rewrite the terminal gateway for multi-channel**

Replace the inbound/outbound internals in `plugins/terminal/terminal.go`:

```go
type Terminal struct {
	mu      sync.Mutex
	pending map[string][]contracts.Message // channel id -> queued inbound lines
	nextID  int
	out     chan tui.RoutedEvent

	ctrlMu sync.Mutex
	ctrl   contracts.SessionControl // set by BindSessionControl (Task 6); nil-safe here
}

var (
	_ contracts.Gateway         = (*Terminal)(nil)
	_ contracts.ChannelReader   = (*Terminal)(nil)
	_ contracts.EventSink       = (*Terminal)(nil)
	_ contracts.RoutedEventSink = (*Terminal)(nil)
	_ contracts.Foreground      = (*Terminal)(nil)
)

func New() *Terminal {
	return &Terminal{pending: map[string][]contracts.Message{}, out: make(chan tui.RoutedEvent, 256)}
}

// Submit enqueues a line the user typed in the TUI as an inbound message on the
// given channel (the active tab's session channel).
func (t *Terminal) Submit(channel, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	t.pending[channel] = append(t.pending[channel], contracts.Message{
		ID:         "t" + strconv.Itoa(t.nextID),
		ChannelID:  channel,
		Content:    text,
		AuthorID:   "local",
		AuthorName: "you",
	})
}

func (t *Terminal) Frontend() <-chan tui.RoutedEvent { return t.out }

// Read drains and returns the lines queued for channelID since the last Read.
func (t *Terminal) Read(_ context.Context, channelID string, _ int, _ string) ([]contracts.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.pending[channelID]
	if len(out) == 0 {
		return nil, nil
	}
	delete(t.pending, channelID)
	return out, nil
}

// EmitTo routes a turn event to the conversation's tab in the TUI.
func (t *Terminal) EmitTo(conv contracts.Conversation, e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: conv, Event: e})
}

// Emit (legacy EventSink) routes to the default single channel.
func (t *Terminal) Emit(e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: contracts.Conversation{Gateway: "terminal", ID: ChannelID}, Event: e})
}

func (t *Terminal) emit(re tui.RoutedEvent) {
	select {
	case t.out <- re:
	default:
	}
}
```

Update `Post`/`Reply`/`Menu`/`UpsertStatusMessage` to route via `EmitTo` using their `Conversation`/channel argument. For `Post`/`Reply` (they receive `conv contracts.Conversation`):

```go
func (t *Terminal) Post(_ context.Context, conv contracts.Conversation, text string) (contracts.MessageID, error) {
	t.EmitTo(conv, contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}

func (t *Terminal) Reply(_ context.Context, conv contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	t.EmitTo(conv, contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}
```

For `UpsertStatusMessage(ctx, channelID, messageID, content)` route to that channel:

```go
func (t *Terminal) UpsertStatusMessage(_ context.Context, channelID, _, content string) (string, error) {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: channelID}, contracts.Event{T: "status", Text: content})
	return "", nil
}
```

Add a stub `Sessions()` that returns nil for now (real impl in Task 6):

```go
// Sessions returns the hub's sessions for tab labels (nil until SessionControl
// is bound — see BindSessionControl).
func (t *Terminal) Sessions() []contracts.SessionInfo {
	t.ctrlMu.Lock()
	c := t.ctrl
	t.ctrlMu.Unlock()
	if c == nil {
		return nil
	}
	return c.Sessions()
}
```

Keep `Menu` routing to its `conv`. Keep `EnsureChannel`, `Enabled`, `DefaultChannel`, `Unreact`, `Manifest`, `React`, `RunForeground` as-is.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./plugins/terminal/...`
Expected: PASS (gateway tests green; tui still compiles with the single-tab model forwarding `e.Event`).

- [ ] **Step 6: Build the whole module**

Run: `go build ./...`
Expected: success (serve.go uses `Submit`/`Frontend` only through the TUI seam — verify the TUI `Run` still calls `m.tm.Submit(...)`; it will be updated in Task 4. If `serve`/tui reference the old `Submit(text)`, this build flags them — fix call sites to the new arity as part of Task 4. For Task 3, it is acceptable that only `./plugins/terminal/...` compiles if the TUI consumer is updated in Task 4; if `go build ./...` fails solely in `tui` model code, proceed to Task 4 which fixes it, then re-run.)

- [ ] **Step 7: Commit**

```bash
git add plugins/terminal/terminal.go plugins/terminal/tui/tui.go plugins/terminal/terminal_test.go
git commit -m "feat(terminal): multi-channel gateway — per-channel queues, EmitTo, routed Frontend

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 4: Multi-tab TUI model

**Files:**
- Modify: `plugins/terminal/tui/tui.go`
- Test: `plugins/terminal/tui/tui_test.go`

**Interfaces:**
- Consumes: `tui.Backend`, `tui.RoutedEvent` (Task 3).
- Produces: a `model` holding `tabs map[string]*tab`, `order []string`, `active string`; helpers `routeEvent(RoutedEvent)`, `switchTab(delta int)`, `ensureTab(channel string)`. Existing per-tab rendering (`renderEvent`, cost/abandoned) preserved per tab.

- [ ] **Step 1: Write the failing tests**

Rewrite `plugins/terminal/tui/tui_test.go` to drive the multi-tab model:

```go
package tui

import (
	"strings"
	"testing"

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

func TestRenderEventMarksAbandonedPerTab(t *testing.T) {
	m := newTestModel()
	m.route(RoutedEvent{Conv: contracts.Conversation{ID: "a"}, Event: contracts.Event{T: "abandoned"}})
	if !strings.Contains(strings.Join(m.tabs["a"].lines, "\n"), "abandoned") {
		t.Fatal("abandoned not surfaced")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./plugins/terminal/tui/ -v`
Expected: COMPILE FAIL — `newModel`, `model.route`, `model.ensureTab`, `model.switchTab`, `tab` undefined.

- [ ] **Step 3: Rewrite the TUI model**

Rewrite `plugins/terminal/tui/tui.go` keeping the package doc, styles, `RoutedEvent`, and `Backend` from Task 3, and replacing the model with a multi-tab one:

```go
// tab is one session's pane: its transcript and unread flag.
type tab struct {
	channel string
	label   string
	lines   []string
	unread  bool
}

type eventMsg RoutedEvent

type model struct {
	tm     Backend
	vp     viewport.Model
	input  textinput.Model
	tabs   map[string]*tab
	order  []string
	active string
	ready  bool
}

func newModel(tm Backend) *model {
	in := textinput.New()
	in.Placeholder = "type a message…"
	in.Focus()
	return &model{tm: tm, input: in, tabs: map[string]*tab{}}
}

// ensureTab creates a tab for channel if missing, making the first tab active.
func (m *model) ensureTab(channel string) *tab {
	if tb, ok := m.tabs[channel]; ok {
		return tb
	}
	tb := &tab{channel: channel, label: channel}
	m.tabs[channel] = tb
	m.order = append(m.order, channel)
	if m.active == "" {
		m.active = channel
	}
	return tb
}

// route delivers a routed event to its tab, marking inactive tabs unread.
func (m *model) route(re RoutedEvent) {
	tb := m.ensureTab(re.Conv.ID)
	before := len(tb.lines)
	m.renderInto(tb, re.Event)
	if len(tb.lines) != before && tb.channel != m.active {
		tb.unread = true
	}
	if tb.channel == m.active {
		m.syncViewport()
	}
}

func (m *model) switchTab(delta int) {
	if len(m.order) == 0 {
		return
	}
	idx := 0
	for i, ch := range m.order {
		if ch == m.active {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(m.order)) % len(m.order)
	m.active = m.order[idx]
	m.tabs[m.active].unread = false
	m.syncViewport()
}

func (m *model) renderInto(tb *tab, e contracts.Event) {
	switch e.T {
	case "chunk":
		tb.lines = append(tb.lines, e.Text)
	case "status":
		tb.lines = append(tb.lines, statusStyle.Render("· "+e.Text))
	case "reply":
		if e.Text != "" {
			tb.lines = append(tb.lines, replyStyle.Render(e.Text))
		}
		if e.Cost > 0 {
			tb.lines = append(tb.lines, costStyle.Render(formatCost(e.Cost)))
		}
	case "reset":
		tb.lines = append(tb.lines, statusStyle.Render("· (turn reset)"))
	case "abandoned":
		tb.lines = append(tb.lines, statusStyle.Render("· (turn abandoned)"))
	}
}

func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	tb := m.tabs[m.active]
	if tb == nil {
		m.vp.SetContent("")
		return
	}
	m.vp.SetContent(strings.Join(tb.lines, "\n"))
	m.vp.GotoBottom()
}

// tabBar renders the tab strip: active tab highlighted, unread marked with •.
func (m *model) tabBar() string {
	var b strings.Builder
	for _, ch := range m.order {
		tb := m.tabs[ch]
		name := tb.label
		if tb.unread {
			name = "•" + name
		}
		if ch == m.active {
			b.WriteString(humanStyle.Render("[" + name + "] "))
		} else {
			b.WriteString(statusStyle.Render(" " + name + " "))
		}
	}
	return b.String()
}
```

Now wire `Init`/`Update`/`View`/`Run`:

```go
func Run(ctx context.Context, cancel context.CancelFunc, tm Backend) error {
	m := newModel(tm)
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-tm.Frontend():
				if !ok {
					return
				}
				p.Send(eventMsg(e))
			}
		}
	}()
	_, err := p.Run()
	cancel()
	return err
}

func (m *model) Init() tea.Cmd { return textinput.Blink }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-4)
			m.ready = true
			m.syncViewport()
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 4
		}
		m.input.Width = msg.Width - 2
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyTab:
			m.switchTab(1)
			return m, nil
		case tea.KeyShiftTab:
			m.switchTab(-1)
			return m, nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text != "" && m.active != "" {
				m.tm.Submit(m.active, text)
				tb := m.tabs[m.active]
				tb.lines = append(tb.lines, humanStyle.Render("you ")+text)
				m.syncViewport()
				m.input.Reset()
			}
		}
	case eventMsg:
		m.route(RoutedEvent(msg))
	}
	var cmds []tea.Cmd
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.vp, c = m.vp.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if !m.ready {
		return "starting…"
	}
	return fmt.Sprintf("%s\n%s\n%s", m.tabBar(), m.vp.View(), m.input.View())
}
```

Note: `tea.NewProgram(m, …)` now takes `*model` (pointer) since methods have pointer receivers; ensure `Init/Update/View` use pointer receivers consistently. Bubbletea accepts a pointer model.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./plugins/terminal/tui/ -v`
Expected: PASS.

- [ ] **Step 5: Build and test the whole module**

Run: `go build ./... && go test ./plugins/terminal/...`
Expected: success and PASS. Fix any stale `Submit(text)`/`Frontend()` call sites surfaced.

- [ ] **Step 6: Commit**

```bash
git add plugins/terminal/tui/tui.go plugins/terminal/tui/tui_test.go
git commit -m "feat(terminal/tui): multi-tab model with per-session panes, Tab switching, unread

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 5: Phase 1 integration check

**Files:**
- Modify: `README.md` (document `--gateways terminal` multi-tab behavior, one paragraph)
- Test: `go test ./...` (whole repo) + manual smoke

**Interfaces:** none new.

- [ ] **Step 1: Run the whole suite**

Run: `go test ./...`
Expected: PASS, except the 6 known Windows path-separator failures (baseline; see project memory). Confirm no NEW failures.

- [ ] **Step 2: Manual smoke (documented, optional on CI)**

From repo root with a terminal-capable build:
1. `go build -o herrscher.exe .`
2. Ensure two terminal-bound sessions exist (e.g. `herrscher session create --name a --terminal_only --shared`, same for `b`) — requires a terminal home, which lands in Phase 2; until then, smoke with one session.
Expected: the TUI shows a tab per terminal-bound session; typing routes to the active tab; replies stream into the right pane; `Tab` switches and clears unread.

- [ ] **Step 3: Document and commit**

Add a short README paragraph under the terminal section describing multi-tab behavior and `Tab`/`Shift+Tab`.

```bash
git add README.md
git commit -m "docs: terminal TUI multi-tab usage (phase 1)

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

## Phase 2 — Create/manage sessions from the TUI

End state: with no Discord configured, `herrscher serve` on a TTY lets the operator `/session create|list|close` entirely from the TUI; new sessions appear/disappear as tabs live.

### Task 6: Terminal `ChannelAdmin` (synthetic channels) + `SessionControlReceiver`

**Files:**
- Modify: `plugins/terminal/terminal.go`
- Test: `plugins/terminal/terminal_test.go`

**Interfaces:**
- Produces: `Terminal` implements `contracts.ChannelAdmin` (`Kind`, `CreateUnder` → `"terminal/<slug>"` unique id, `ForumPost`, `Archive`, `Send`) and `contracts.SessionControlReceiver` (`BindSessionControl`).
- Produces: `newGatewaySet` returns a `GatewaySet` with `Admin: tm` so `firstAdmin` resolves to the terminal when no Discord is present.
- Consumes: `contracts.SessionControl`, `contracts.ChannelAdmin`.

- [ ] **Step 1: Write the failing tests**

Add to `plugins/terminal/terminal_test.go`:

```go
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
```

Add `"strings"` to the test imports if missing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./plugins/terminal/ -run 'ChannelAdmin|CreateUnder|Archive|GatewaySetExposesAdmin' -v`
Expected: FAIL — methods/fields missing.

- [ ] **Step 3: Implement `ChannelAdmin` + `SessionControlReceiver`**

Add to `plugins/terminal/terminal.go`:

```go
var (
	_ contracts.ChannelAdmin           = (*Terminal)(nil)
	_ contracts.SessionControlReceiver = (*Terminal)(nil)
)

// BindSessionControl stores the hub controller so the TUI can drive the session
// lifecycle (create/close/list) and enumerate sessions for tab labels.
func (t *Terminal) BindSessionControl(c contracts.SessionControl) {
	t.ctrlMu.Lock()
	t.ctrl = c
	t.ctrlMu.Unlock()
}

// Control exposes the bound SessionControl to the TUI (nil before bind).
func (t *Terminal) Control() contracts.SessionControl {
	t.ctrlMu.Lock()
	defer t.ctrlMu.Unlock()
	return t.ctrl
}

// --- contracts.ChannelAdmin: synthetic, terminal-local channels ---

func (t *Terminal) Kind(_ context.Context, _ string) (string, error) { return "text", nil }

func (t *Terminal) CreateUnder(_ context.Context, _ , name string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	return "terminal/" + slug(name) + "-" + strconv.Itoa(t.nextID), nil
}

func (t *Terminal) ForumPost(ctx context.Context, parentID, name, _ string) (string, error) {
	return t.CreateUnder(ctx, parentID, name)
}

func (t *Terminal) Archive(_ context.Context, id string) error {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: id}, contracts.Event{T: "closed"})
	return nil
}

func (t *Terminal) Send(_ context.Context, channelID, content string) error {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: channelID}, contracts.Event{T: "status", Text: content})
	return nil
}

// slug lowercases and replaces unsafe runes so a channel id stays path-safe.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	return out
}
```

Update `newGatewaySet` to expose the admin:

```go
func newGatewaySet(ctx context.Context, cfg contracts.PluginConfig) (contracts.GatewaySet, error) {
	tm := New()
	return contracts.GatewaySet{Gateway: tm, Reader: tm, Admin: tm}, nil
}
```

Add `"strings"` to the terminal.go imports. The TUI must render the new `"closed"` event (drop the tab) — that is handled in Task 9; for now `renderInto` ignores unknown types harmlessly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./plugins/terminal/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/terminal/terminal.go plugins/terminal/terminal_test.go
git commit -m "feat(terminal): ChannelAdmin synthetic channels + SessionControlReceiver

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 7: Generalize session creation off Discord (manager)

**Files:**
- Modify: `core/internal/manager/session.go`
- Modify: `core/internal/manager/ports.go` (rename `discord` → `channelAdmin`, doc only)
- Test: `core/internal/manager/handler_test.go` (add a terminal-home case)

**Interfaces:**
- Consumes: `state.HomeRef{Type: "terminal"}`, the injected `channelAdmin` port (`firstAdmin`).
- Produces: `sessionCreateRun` supports `home.Type == "terminal"` (mints channel via `CreateUnder`, `Type: "text"`), and emits platform-neutral banner/result text (no `<#…>`/`<@…>` markup).

- [ ] **Step 1: Write the failing test**

Inspect `core/internal/manager/handler_test.go` for the existing fake admin (`discord`) used in create tests. Add a test that sets a terminal home and asserts creation succeeds with a terminal channel and neutral output. Sketch (adapt names to the existing fakes):

```go
func TestSessionCreateTerminalHome(t *testing.T) {
	h, fake := newTestHandler(t) // existing helper that injects a fake admin + state
	_ = h.st.SetHome(state.HomeRef{ID: "term-home", Type: "terminal"})

	out, err := h.sessionCreateRun(context.Background(), inputWith(map[string]string{
		"name":          "alpha",
		"terminal_only": "true",
		"shared":        "true",
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess, ok := h.st.FindSession("alpha")
	if !ok {
		t.Fatal("session not persisted")
	}
	if sess.Type != "text" || sess.ChannelID == "" {
		t.Fatalf("bad session: %+v", sess)
	}
	if strings.Contains(out, "<#") || strings.Contains(out, "<@") {
		t.Fatalf("output must be platform-neutral: %q", out)
	}
	if len(fake.created) == 0 {
		t.Fatal("CreateUnder should have been called for terminal home")
	}
}
```

If no `newTestHandler`/`inputWith` helpers exist, reuse the patterns already in `handler_test.go` (it is 783 lines — there is an established fake admin and input builder; match them exactly).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core/internal/manager/ -run TestSessionCreateTerminalHome -v`
Expected: FAIL — `home.Type "terminal" unsupported`.

- [ ] **Step 3: Support the terminal home + neutralize output**

In `sessionCreateRun` (`session.go:147`), extend the switch:

```go
switch home.Type {
case "category", "terminal":
	chID, err := h.d.CreateUnder(ctx, home.ID, title)
	if err != nil {
		rollbackWorktree()
		return "", fmt.Errorf("create channel: %v", err)
	}
	sess = state.Session{Name: name, ChannelID: chID, Type: "text", Cmd: cmd, Backend: backend, Worktree: worktree, Project: project, Agent: agentName, Gateways: gateways, Extractor: extractor, Journal: journal, ConsolidateEvery: consolidateEvery}
case "forum":
	// unchanged
	...
default:
	return "", fmt.Errorf("home type %q unsupported", home.Type)
}
```

Make the final result string neutral (the banner is already mostly neutral; only the result line uses `<#…>`):

```go
banner := sessionBanner(repo, name, worktree, h.wt.Branch(name), cmd, shared)
_ = h.d.Send(ctx, sess.ChannelID, banner) // best-effort
return fmt.Sprintf("✅ Session **%s** running on %s.\n\n%s", name, sess.ChannelID, banner), nil
```

(Discord still renders fine with the bare channel id in text; the gateway, not the core, owns mention formatting.) Rename the `discord` interface to `channelAdmin` in `ports.go` (doc/clarity only — update the field type in `Handler` and `NewHandler` accordingly; it is structurally identical to `contracts.ChannelAdmin`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core/internal/manager/...`
Expected: PASS (existing Discord create tests still green — `category`/`forum` unchanged).

- [ ] **Step 5: Commit**

```bash
git add core/internal/manager/session.go core/internal/manager/ports.go core/internal/manager/handler_test.go
git commit -m "feat(manager): support terminal home + platform-neutral session output

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 8: Seed a terminal home + default-bind terminal in foreground mode

**Files:**
- Modify: `core/host/serve.go` (seed a terminal home when terminal is foreground and no home set)
- Modify: `core/internal/manager/gateways.go` (default to terminal when terminal is the only/foreground gateway — via an explicit option, not magic)
- Test: `core/internal/manager/gateways_test.go` (new or existing), `core/host/serve_test.go` if present

**Interfaces:**
- Consumes: `host.Options`, `state.HomeRef`.
- Produces: when `RunHub` runs with a foreground terminal gateway and `st.Home.ID == ""`, it seeds `HomeRef{ID: "terminal", Type: "terminal"}`. `ParseGateways("", terminalOnly=false)` still defaults to `["discord"]`; a new boolean wiring lets the TUI pass `terminal_only` by default (handled in Task 9 by the TUI prepending the flag), so no behavior change is forced on the Discord path.

- [ ] **Step 1: Write the failing test**

Add `core/host/serve_test.go` (or extend an existing host test) asserting the home-seed helper:

```go
func TestSeedTerminalHomeWhenForeground(t *testing.T) {
	st := state.NewState(filepath.Join(t.TempDir(), "state.json"))
	seedTerminalHome(st, true /* hasForeground */)
	if st.Home.Type != "terminal" || st.Home.ID == "" {
		t.Fatalf("home not seeded: %+v", st.Home)
	}
	// Does not overwrite an existing home.
	st2 := state.NewState(filepath.Join(t.TempDir(), "s2.json"))
	_ = st2.SetHome(state.HomeRef{ID: "disc", Type: "category"})
	seedTerminalHome(st2, true)
	if st2.Home.Type != "category" {
		t.Fatalf("existing home overwritten: %+v", st2.Home)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core/host/ -run TestSeedTerminalHomeWhenForeground -v`
Expected: FAIL — `seedTerminalHome` undefined.

- [ ] **Step 3: Implement the seed + call it**

Add to `core/host/serve.go`:

```go
// seedTerminalHome sets a terminal home when a foreground (TUI) gateway is bound
// and no home is configured, so session create works with no Discord. It never
// overwrites an existing home (a Discord setup keeps its category/forum).
func seedTerminalHome(st *state.State, hasForeground bool) {
	if !hasForeground || st.Home.ID != "" {
		return
	}
	_ = st.SetHome(state.HomeRef{ID: "terminal", Type: "terminal"})
}
```

Call it in `RunHub` after `st.ApplyDefaults(...)` and before `buildRegistry`, passing whether a foreground gateway is present. `RunHub` currently does not know about foreground; pass it via `Options` (add `ForegroundBound bool`) set in `serve.go:runServe` where `fg != nil` is computed. Wire `opts.ForegroundBound = fg != nil`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core/host/...`
Expected: PASS.

- [ ] **Step 5: Build the module**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add core/host/serve.go core/host/serve_test.go
git commit -m "feat(host): seed terminal home for foreground TUI when no Discord home set

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 9: TUI slash-command dispatch + live tab create/close

**Files:**
- Modify: `plugins/terminal/tui/tui.go`
- Test: `plugins/terminal/tui/tui_test.go`

**Interfaces:**
- Consumes: `Backend.Sessions()`, and a new `Backend` method to dispatch commands: extend `Backend` with `Dispatch(args []string) (string, error)` (terminal implements it by delegating to the bound `SessionControl`, prepending nothing). The terminal’s `Dispatch` calls `Control().Dispatch(ctx, args)`.
- Produces: typing a line starting with `/` is parsed to argv and dispatched (not submitted as a prompt); the TUI refreshes tabs from `Sessions()` after dispatch and on a periodic tick; a `"closed"` event removes the tab.

- [ ] **Step 1: Extend the Backend seam + terminal impl (no test yet)**

In `tui.go` `Backend`:

```go
type Backend interface {
	Frontend() <-chan RoutedEvent
	Submit(channel, text string)
	Sessions() []contracts.SessionInfo
	Dispatch(args []string) (string, error)
}
```

In `plugins/terminal/terminal.go`:

```go
func (t *Terminal) Dispatch(args []string) (string, error) {
	c := t.Control()
	if c == nil {
		return "", fmt.Errorf("session control not bound")
	}
	return c.Dispatch(context.Background(), args)
}
```

Add `"fmt"` to terminal.go imports if missing.

- [ ] **Step 2: Write the failing tests**

Add to `plugins/terminal/tui/tui_test.go` a fake backend and tests:

```go
type fakeBackend struct {
	dispatched [][]string
	sessions   []contracts.SessionInfo
	fe         chan RoutedEvent
}

func (f *fakeBackend) Frontend() <-chan RoutedEvent { return f.fe }
func (f *fakeBackend) Submit(string, string)        {}
func (f *fakeBackend) Sessions() []contracts.SessionInfo { return f.sessions }
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./plugins/terminal/tui/ -run 'Slash|PlainLine|ClosedEvent|SyncTabs' -v`
Expected: FAIL — `handleEnter`, `syncTabs`, closed-event removal, label-from-session missing.

- [ ] **Step 4: Implement dispatch, syncTabs, closed-tab removal**

Refactor the Enter handling into `handleEnter` and add helpers in `tui.go`:

```go
// handleEnter dispatches a /command or submits a prompt to the active tab.
func (m *model) handleEnter() {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return
	}
	m.input.Reset()
	if strings.HasPrefix(text, "/") {
		args := strings.Fields(strings.TrimPrefix(text, "/"))
		out, err := m.tm.Dispatch(args)
		m.syncTabs()
		tb := m.tabs[m.active]
		if tb != nil {
			line := out
			if err != nil {
				line = "error: " + err.Error()
			}
			tb.lines = append(tb.lines, statusStyle.Render("· "+line))
			m.syncViewport()
		}
		return
	}
	if m.active == "" {
		return
	}
	m.tm.Submit(m.active, text)
	tb := m.tabs[m.active]
	tb.lines = append(tb.lines, humanStyle.Render("you ")+text)
	m.syncViewport()
}

// syncTabs reconciles tabs against the hub's session list: it creates tabs for
// new sessions, labels them by name, and drops tabs whose session is gone.
func (m *model) syncTabs() {
	infos := m.tm.Sessions()
	if infos == nil {
		return
	}
	live := map[string]bool{}
	for _, s := range infos {
		live[s.ChannelID] = true
		tb := m.ensureTab(s.ChannelID)
		if s.Name != "" {
			tb.label = s.Name
		}
	}
	for _, ch := range append([]string(nil), m.order...) {
		if !live[ch] {
			m.removeTab(ch)
		}
	}
}

// removeTab drops a tab and fixes the active selection.
func (m *model) removeTab(channel string) {
	if _, ok := m.tabs[channel]; !ok {
		return
	}
	delete(m.tabs, channel)
	out := m.order[:0]
	for _, ch := range m.order {
		if ch != channel {
			out = append(out, ch)
		}
	}
	m.order = out
	if m.active == channel {
		m.active = ""
		if len(m.order) > 0 {
			m.active = m.order[0]
		}
		m.syncViewport()
	}
}
```

Handle the `"closed"` event in `route` before rendering:

```go
func (m *model) route(re RoutedEvent) {
	if re.Event.T == "closed" {
		m.removeTab(re.Conv.ID)
		return
	}
	tb := m.ensureTab(re.Conv.ID)
	...
}
```

Update `Update`'s `KeyEnter` case to call `m.handleEnter()`. Add a periodic refresh: in `Init` return `tea.Batch(textinput.Blink, tickCmd())` with a `tickMsg` every ~1s that calls `m.syncTabs()` and re-arms; this keeps tabs in sync when sessions change via the CLI. (Define `tickMsg`, `tickCmd` using `tea.Tick`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./plugins/terminal/...`
Expected: PASS.

- [ ] **Step 6: Build + whole suite**

Run: `go build ./... && go test ./...`
Expected: success; only the known Windows baseline failures.

- [ ] **Step 7: Commit**

```bash
git add plugins/terminal/tui/tui.go plugins/terminal/tui/tui_test.go plugins/terminal/terminal.go
git commit -m "feat(terminal/tui): slash-command dispatch + live tab create/close

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 10: Phase 2 integration check

**Files:**
- Modify: `README.md` (document creating/closing sessions from the TUI, terminal-only mode)

- [ ] **Step 1: Whole suite**

Run: `go test ./...`
Expected: PASS apart from the Windows baseline failures; no new failures.

- [ ] **Step 2: Manual smoke**

1. Build: `go build -o herrscher.exe .`
2. With NO `DISCORD_BOT_TOKEN` set, run `./herrscher.exe serve` on a TTY.
3. In the TUI: `/session create --name a --terminal_only --shared`, then `/session create --name b --terminal_only --shared`.
Expected: two tabs `a` and `b` appear; typing routes to the active session and streams replies into its pane; `/session close --name b` removes tab `b`.

- [ ] **Step 3: Document + commit**

```bash
git add README.md
git commit -m "docs: manage terminal sessions from the TUI (phase 2)

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

## Phase 3 — UX polish

End state: comfortable daily multi-agent operation — activity/unread indicators, per-tab status/cost, close confirmation, scrollback, keybinding help, graceful session-death handling.

### Task 11: Per-tab activity + status/cost footer

**Files:**
- Modify: `plugins/terminal/tui/tui.go`
- Test: `plugins/terminal/tui/tui_test.go`

**Interfaces:**
- Produces: each `tab` tracks `busy bool` (a turn is streaming: set on `status`/`chunk`, cleared on `reply` with `Done`) and `lastCost float64`; `View` renders a footer line for the active tab (`busy ⟳ / idle ·`, last cost). The tab bar shows `⟳` next to busy tabs.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugins/terminal/tui/ -run TestTabBusyLifecycle -v`
Expected: FAIL — `busy`/`lastCost` fields missing.

- [ ] **Step 3: Implement busy/cost tracking + footer**

Add `busy bool` and `lastCost float64` to `tab`. In `renderInto` (or `route`), set `tb.busy = true` on `status`/`chunk`, and on `reply` with `Done` set `tb.busy = false` and `tb.lastCost = e.Cost` when `e.Cost > 0`. Add a footer in `View`:

```go
func (m *model) footer() string {
	tb := m.tabs[m.active]
	if tb == nil {
		return ""
	}
	state := statusStyle.Render("· idle")
	if tb.busy {
		state = humanStyle.Render("⟳ working")
	}
	cost := ""
	if tb.lastCost > 0 {
		cost = "  " + costStyle.Render("last "+formatCost(tb.lastCost))
	}
	return state + cost
}
```

Render it in `View`: `fmt.Sprintf("%s\n%s\n%s\n%s", m.tabBar(), m.vp.View(), m.footer(), m.input.View())` and adjust the viewport height to `msg.Height-5`. Add `⟳` to busy tabs in `tabBar`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugins/terminal/tui/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/terminal/tui/tui.go plugins/terminal/tui/tui_test.go
git commit -m "feat(terminal/tui): per-tab busy + cost footer and activity markers

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 12: Scrollback, close confirmation, keybinding help, session-death

**Files:**
- Modify: `plugins/terminal/tui/tui.go`
- Test: `plugins/terminal/tui/tui_test.go`

**Interfaces:**
- Produces: `PgUp`/`PgDn` scroll the active tab's viewport (delegated to `viewport.Model`); `Ctrl+W` asks a one-key confirm then dispatches `/session close --name <active label>`; `?` toggles a help overlay listing keys; an `abandoned` event marks the tab footer `disconnected` until the next event.

- [ ] **Step 1: Write the failing tests**

```go
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
	if len(f.dispatched) != 1 || f.dispatched[0][0] != "session" || f.dispatched[0][1] != "close" {
		t.Fatalf("close not dispatched: %+v", f.dispatched)
	}
	found := false
	for _, a := range f.dispatched[0] {
		if a == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("close must target the active session name: %+v", f.dispatched[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./plugins/terminal/tui/ -run 'HelpToggle|CloseActive' -v`
Expected: FAIL — `showHelp`, `toggleHelp`, `confirmClose` missing.

- [ ] **Step 3: Implement help, confirm-close, scrollback, disconnected**

Add `showHelp bool` to `model`; `toggleHelp()` flips it; `View` prepends a help block when set. Add:

```go
// confirmClose dispatches a close for the active tab's session by label (name).
func (m *model) confirmClose() {
	tb := m.tabs[m.active]
	if tb == nil || tb.label == "" {
		return
	}
	_, _ = m.tm.Dispatch([]string{"session", "close", "--name", tb.label})
	m.syncTabs()
}
```

In `Update`'s `KeyMsg`, add `tea.KeyPgUp`/`tea.KeyPgDown` (let `m.vp.Update(msg)` handle it — viewport already supports it; just don't intercept), a `Ctrl+W` two-step confirm (set `m.pendingClose = true`; next `y` calls `confirmClose`, any other key cancels), and a rune `?` to `toggleHelp`. For `abandoned`, set a `tab.disconnected` flag rendered in the footer; clear it on the next non-abandoned event for that tab.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./plugins/terminal/tui/...`
Expected: PASS.

- [ ] **Step 5: Build + whole suite**

Run: `go build ./... && go test ./...`
Expected: success; only Windows baseline failures.

- [ ] **Step 6: Commit**

```bash
git add plugins/terminal/tui/tui.go plugins/terminal/tui/tui_test.go
git commit -m "feat(terminal/tui): scrollback, close confirm, help overlay, disconnect marker

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

### Task 13: Phase 3 polish wrap-up + docs

**Files:**
- Modify: `README.md` (keybinding reference table for the TUI)

- [ ] **Step 1: Whole suite**

Run: `go test ./...`
Expected: PASS apart from Windows baseline failures.

- [ ] **Step 2: Manual smoke of the full UX**

Drive the TUI: create 3 sessions, run turns in each, observe activity markers + per-tab cost, scroll back, `?` help, `Ctrl+W`+`y` close, and a session whose bridge dies showing `disconnected`.

- [ ] **Step 3: Document + commit**

```bash
git add README.md
git commit -m "docs: terminal TUI keybindings + multi-session workflow (phase 3)

Claude-Session: https://claude.ai/code/session_016Mh9Ap83k7osBhj5iTJU68"
```

---

## Plan self-review notes

- **Spec coverage:** RoutedEventSink (Task 1) + fanOut (Task 2) cover decision 1; Degrade passthrough (Task 1) covers decision 2; multi-channel gateway (Task 3) + ChannelAdmin (Task 6) cover decisions 3; routed Frontend (Task 3) covers decision 4; Sessions()/SessionControlReceiver (Tasks 6, 9) cover decision 5; manager generalization + terminal home (Tasks 7, 8) cover decision 6. Phases map 1↔Tasks 1-5, 2↔Tasks 6-10, 3↔Tasks 11-13. Testing-strategy items map to the per-task tests; purity is re-checked in Tasks 5/9/12 whole-suite runs.
- **Type consistency:** `Backend` grows monotonically (Task 3 adds `Frontend/Submit/Sessions`; Task 9 adds `Dispatch`) — `fakeBackend` in Task 9 implements all four. `RoutedEvent`, `tab`, `model.route/ensureTab/switchTab/syncTabs/removeTab/handleEnter/confirmClose/toggleHelp` names are used consistently across tasks. `EmitTo(conv, e)` signature matches contracts Task 1 and host Task 2.
- **Open verification during execution:** confirm the exact fake-admin/input helpers in `handler_test.go` (Task 7) and match them rather than the sketched names; confirm `tea.NewProgram` is given a pointer `*model` consistently (pointer receivers).
