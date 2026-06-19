# Phase 3 — Pure-Runner Hub + Terminal Gateway + Bubbletea TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `herrscherd serve` opens an in-process Bubbletea TUI; the terminal is a first-class gateway peer to Discord; the daemon becomes a multi-gateway hub that owns all gateway I/O and fans each turn out to every bound gateway; the `bridge` subprocess becomes a pure backend runner that talks only over a bidirectional control socket.

**Architecture:** The control socket is inverted and made persistent — the daemon **accepts**, the bridge **dials+redials**. The bridge, in "hub mode", stops polling gateways: it reads `input`/`pick` frames from the socket, runs the backend, and emits `human`/`chunk`/`status`/`reply`/`reset` frames back. The daemon hub runs one FIFO turn-driver per session: it polls every bound gateway's `Read`, enqueues inputs, writes them to the bridge one turn at a time, and fans each turn event out to all bound gateways. A gateway renders the stream itself if it implements the new optional `contracts.EventSink` capability (the terminal does, driving the TUI); otherwise a host-side agnostic renderer reproduces the current Discord behavior (progress view, threading, reactions) using only contracts ports. Built in 6 always-green milestones; the terminal MVP lights up at the end of M4 and Discord re-attaches in M5.

**Tech Stack:** Go 1.25, stdlib + `github.com/charmbracelet/bubbletea` / `lipgloss` / `bubbles` (first external UI deps). Module `github.com/Herrscherd/herrscher`; contracts at `github.com/Herrscherd/herrscher-contracts` (local clone `/home/shan/dev/herrscher-contracts`, to be bumped v0.1.1 → v0.1.2).

---

## Conventions for every task

- **Commit identity is mandatory and fixed.** Every `git commit` MUST be:
  ```bash
  git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "<msg>"
  ```
  Never use the ambient identity (it is wrong in this environment).
- **House style:** Go stdlib idioms, minimal comments (only where the *why* is non-obvious), French acceptable in prose. No new external deps beyond the Bubbletea trio in M4.
- **Test command (run from `/home/shan/dev/herrscher`):** `go test ./...`. The purity guards `TestCorePurity` (`core/purity_test.go`) and `TestHostPurity` (`purity_test.go`) MUST stay green in every milestone.
- **Working dir:** `/home/shan/dev/herrscher` unless a step says otherwise (M0 steps run in `/home/shan/dev/herrscher-contracts`).

---

## File Structure

**Milestone 0 — contracts v0.1.2** (repo `/home/shan/dev/herrscher-contracts`)
- Create `event.go` — moves the `Event` struct here (from `core/internal/control`) + new optional `EventSink` capability interface.
- Modify `go.mod` of herrscher to require v0.1.2; add a temporary `replace` for the dev loop, dropped once tagged.

**Milestone 1 — bidirectional transport** (`core/internal/control/`)
- Create `conn.go` — `Conn` (persistent bidirectional event connection), `Dial` (bridge side), `Accept`/`Acceptor` (daemon side).
- Create `conn_test.go` — round-trip + reconnect tests over a real unix socket.
- Modify `event.go` — `WriteEvent`/`ScanEvents` now operate on `contracts.Event` (the struct moved to contracts in M0); the `Event` type alias is removed.

**Milestone 2 — bridge runner mode** (`core/bridge/`)
- Create `hub.go` — `runHub` (the runner-mode loop: read input frames → backend → emit events) + `runOnce` (one turn).
- Create `hub_test.go` — drives `runHub` over an in-memory conn pair.
- Modify `bridge.go` — `Options.HubSocket`; `Run` dispatches to `runHub` when set; drop the local `EventSink` interface in favor of `contracts.EventSink`.
- Modify `bridge.go` (root) — register `--hub-socket`, pass it through.

**Milestone 3 — terminal gateway plugin** (`plugins/terminal/`)
- Create `terminal.go` — `Terminal` (implements `contracts.Gateway`, `contracts.ChannelReader`, `contracts.EventSink`); `init()` self-registration; package-level `Active()` accessor for the TUI.
- Create `terminal_test.go` — Read drains submitted lines; Emit/Post forward to the frontend channel.
- Modify `plugins.go` (root) — blank-import the terminal plugin.

**Milestone 4 — hub turn-loop + TUI**
- Create `core/host/turnloop.go` — `sessionDriver` (FIFO queue, poll bound gateways, pump to bridge, fan-out), `Hub.run` per session.
- Create `core/host/turnloop_test.go` — FIFO ordering, fan-out to all bound gateways, reconnect resumes next turn.
- Create `plugins/terminal/tui/tui.go` — Bubbletea model rendering the terminal gateway's stream + input.
- Modify `core/host/serve.go` — replace the bare `<-ctx.Done()` with the hub turn-driver wiring; start drivers per session.
- Modify `serve.go` (root) — TTY detection; when interactive, run the TUI; register terminal as a bound gateway.
- Modify `core/internal/supervisor/supervisor.go` — `bridgeArgs` adds `--hub-socket <control.SocketPath(sess.Name)>`.

**Milestone 5 — re-attach Discord through the hub**
- Create `core/host/renderer.go` — `gatewayRenderer` (agnostic, contracts-only) reproducing progress view + threading + reactions from the event stream, for gateways without `EventSink`.
- Move `core/bridge/progress.go` rendering helpers into `core/host/renderer.go` (progressView, postResult, chunk) — relocated, not rewritten.
- Modify `core/bridge/bridge.go` — delete the Discord-polling `Run` path and `handle`; `Run` is now hub-mode only.
- Modify `core/internal/supervisor/supervisor.go` — `bridgeArgs` drops `-c <channel>` (the bridge no longer reads a channel).

**Milestone 6 — cleanup + final review**
- Modify `core/internal/control/control.go` — delete the obsolete one-shot `Server`/`Listen`/`Send` pick path (superseded by `Conn`).
- Modify the manager menu-pick routing to send picks over the persistent `Conn` instead of `control.Send`.

---

## Milestone 0 — contracts v0.1.2: move `Event`, add `EventSink`

**Why first:** the terminal plugin (M3) and the hub fan-out (M4) need `Event` to be importable by plugins and a capability to stream it. Plugins cannot import `core/internal/control` (internal). So `Event` moves to contracts and an optional `EventSink` capability is added.

### Task 0.1: Add `Event` + `EventSink` to contracts

**Files:**
- Create: `/home/shan/dev/herrscher-contracts/event.go`
- Test: `/home/shan/dev/herrscher-contracts/event_test.go`

- [ ] **Step 1: Write the failing test** (in `/home/shan/dev/herrscher-contracts`)

```go
package contracts

import "testing"

func TestEventSinkIsOptionalCapability(t *testing.T) {
	// A type implementing EventSink must satisfy the interface; the compiler
	// proves the method set. This test documents the contract shape.
	var _ EventSink = sinkStub{}
	e := Event{T: "chunk", Text: "hi"}
	if e.T != "chunk" || e.Text != "hi" {
		t.Fatalf("Event fields not wired: %+v", e)
	}
}

type sinkStub struct{}

func (sinkStub) Emit(Event) {}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /home/shan/dev/herrscher-contracts && go test ./... -run TestEventSinkIsOptionalCapability`
Expected: FAIL — `undefined: EventSink`, `undefined: Event`.

- [ ] **Step 3: Write the implementation**

Create `/home/shan/dev/herrscher-contracts/event.go`:

```go
package contracts

// Event is one message on the session bus. The bridge (a pure backend runner)
// emits turn events for the hub to fan out; the hub injects input/pick down to
// the bridge. One Event encodes to exactly one JSON line on the wire.
//
// chunk carries assistant prose; status carries a tool/progress line.
//
//	{"t":"human","who":"alice","text":"refactor the env loader"}
//	{"t":"status","text":"reading envfile.go"}
//	{"t":"chunk","text":"proposing 3 changes"}
//	{"t":"reply","text":"done","done":true}
//	{"t":"input","who":"terminal","text":"apply them"}
//	{"t":"pick","value":"2"}
//	{"t":"reset"}  // discard the in-progress turn (backend was reset mid-turn)
type Event struct {
	T     string `json:"t"`
	Who   string `json:"who,omitempty"`
	Text  string `json:"text,omitempty"`
	Value string `json:"value,omitempty"`
	Done  bool   `json:"done,omitempty"`
}

// EventSink is an optional gateway capability: a gateway that renders the live
// turn event stream itself (the terminal TUI does) implements it. The hub fans
// every turn event to each bound gateway implementing EventSink; a gateway
// without it is driven by the host's default renderer, which posts the final
// reply (and a progress view) through the Gateway/ChannelReader ports.
type EventSink interface {
	Emit(Event)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /home/shan/dev/herrscher-contracts && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit** (in the contracts repo)

```bash
cd /home/shan/dev/herrscher-contracts
git add event.go event_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(contracts): add Event type + optional EventSink capability"
```

### Task 0.2: Wire herrscher to local contracts via `replace`, then re-point control

**Files:**
- Modify: `/home/shan/dev/herrscher/go.mod`
- Modify: `/home/shan/dev/herrscher/core/internal/control/event.go`

- [ ] **Step 1: Add a temporary replace so herrscher builds against the local contracts clone**

Run:
```bash
cd /home/shan/dev/herrscher
go mod edit -replace github.com/Herrscherd/herrscher-contracts=/home/shan/dev/herrscher-contracts
go mod tidy
```
Expected: `go.mod` gains `replace github.com/Herrscherd/herrscher-contracts => /home/shan/dev/herrscher-contracts`.

- [ ] **Step 2: Re-point `core/internal/control/event.go` at `contracts.Event`**

Replace the whole file `core/internal/control/event.go` with (the `Event` struct is gone — it now lives in contracts; the wire helpers stay here and take `contracts.Event`):

```go
package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// WriteEvent encodes e as a single JSON line (newline-terminated). encoding/json
// escapes any newline inside a field, so one Event is always one line.
func WriteEvent(w io.Writer, e contracts.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ScanEvents reads JSON-line events from r, calling fn for each. A blank line is
// skipped; a line starting with '{' MUST parse as a JSON Event or it is a
// protocol error (returned, not surfaced as a pick); a non-'{' line is treated
// as the legacy bare pick value and surfaced as Event{T:"pick", Value:line} for
// back-compat. It returns the first fn error, a malformed-line error, or a read
// error other than io.EOF.
func ScanEvents(r io.Reader, fn func(contracts.Event) error) error {
	sc := bufio.NewScanner(r)
	// 1 MiB line cap: a longer line ends the scan with bufio.ErrTooLong. This is
	// a deliberate protocol limit.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line[0] == '{' {
			var e contracts.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return fmt.Errorf("control: malformed event line: %w", err)
			}
			if err := fn(e); err != nil {
				return err
			}
			continue
		}
		if err := fn(contracts.Event{T: "pick", Value: line}); err != nil {
			return err
		}
	}
	return sc.Err()
}
```

- [ ] **Step 3: Update the existing event test to use `contracts.Event`**

In `core/internal/control/event_test.go`, replace every bare `Event{` with `contracts.Event{` and add the import `contracts "github.com/Herrscherd/herrscher-contracts"`. (Open the file, apply the rename across all occurrences; the assertions are otherwise unchanged.)

- [ ] **Step 4: Update the bridge to use `contracts.Event` and `contracts.EventSink`**

In `core/bridge/bridge.go`:
- Delete the local `EventSink` interface (lines defining `type EventSink interface { Emit(control.Event) }`).
- Change the `runner.sink` field type from `EventSink` to `contracts.EventSink`.
- Change `emit`'s parameter and every `control.Event{...}` literal in `emit`/`emitBackend`/`handle` to `contracts.Event{...}`.
- Change `Run`'s parameter `sink EventSink` to `sink contracts.EventSink`.

In `core/bridge/runner_test.go`:
- `fakeSink.Emit` takes `contracts.Event`; the `[]control.Event` slices become `[]contracts.Event`; drop the now-unused `control` import if nothing else uses it (it does not after this change).

In `bridge.go` (root), `bridge.Run(... nil, ...)` is unchanged (nil still satisfies `contracts.EventSink`).

- [ ] **Step 5: Run the full suite**

Run: `cd /home/shan/dev/herrscher && go test ./...`
Expected: PASS (244 tests green), including `TestCorePurity`/`TestHostPurity`.

- [ ] **Step 6: Commit**

```bash
cd /home/shan/dev/herrscher
git add go.mod go.sum core/internal/control/event.go core/internal/control/event_test.go core/bridge/bridge.go core/bridge/runner_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "refactor(control,bridge): use contracts.Event + contracts.EventSink"
```

> **Note on tagging:** the contracts repo is tagged + `go get`'d and the `replace` dropped at the very end (Task 6.3), so the whole phase develops against the local clone and finalizes once.

---

## Milestone 1 — bidirectional transport: `control.Conn`

**Why:** the hub (daemon) must hold a persistent, two-way connection to each bridge: push `input`/`pick` down, read `chunk`/`status`/`reply` up. Today `control` is one-shot (daemon `Send`s a pick, bridge `Listen`s). M1 adds the persistent API alongside the old one (the old path is removed in M6).

### Task 1.1: `Conn` — a persistent bidirectional event connection

**Files:**
- Create: `core/internal/control/conn.go`
- Test: `core/internal/control/conn_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/internal/control/conn_test.go`:

```go
package control_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
)

func TestConnRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rt.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()

	cli, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	srvConn := <-acc.Conns()

	// daemon → bridge
	if err := srvConn.Write(contracts.Event{T: "input", Who: "alice", Text: "hi"}); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	var got contracts.Event
	done := make(chan struct{})
	go func() {
		_ = cli.Scan(func(e contracts.Event) error { got = e; close(done); return errStop })
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for input frame")
	}
	if got.T != "input" || got.Text != "hi" || got.Who != "alice" {
		t.Fatalf("client got %+v, want input/alice/hi", got)
	}
}

var errStop = stopErr{}

type stopErr struct{}

func (stopErr) Error() string { return "stop" }

func TestConnConcurrentWrites(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "cw.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()
	cli, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()
	srvConn := <-acc.Conns()

	const n = 50
	var got int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		_ = srvConn.Scan(func(contracts.Event) error {
			mu.Lock()
			got++
			reached := got == n
			mu.Unlock()
			if reached {
				close(done)
				return errStop
			}
			return nil
		})
	}()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = cli.Write(contracts.Event{T: "chunk", Text: "x"}) }()
	}
	wg.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("got %d events, want %d (interleaved write corruption?)", got, n)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./core/internal/control/ -run TestConn`
Expected: FAIL — `undefined: control.Accept`, `undefined: control.Dial`.

- [ ] **Step 3: Write the implementation**

Create `core/internal/control/conn.go`:

```go
package control

import (
	"net"
	"os"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// Conn is a persistent bidirectional event connection over a net.Conn. The
// daemon (hub) accepts one per supervised bridge; the bridge dials. Each
// direction is an independent stream of JSON-line Events. Write is safe for
// concurrent callers (serialized by mu); Scan must be called from a single
// goroutine.
type Conn struct {
	c  net.Conn
	mu sync.Mutex
}

func newConn(c net.Conn) *Conn { return &Conn{c: c} }

// Write sends one Event as a JSON line. Safe for concurrent callers.
func (k *Conn) Write(e contracts.Event) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return WriteEvent(k.c, e)
}

// Emit satisfies contracts.EventSink so the bridge can hand a Conn directly to
// its turn loop as the event sink. A write error is dropped: emission is
// best-effort and the hub's read side detects a dead conn independently.
func (k *Conn) Emit(e contracts.Event) { _ = k.Write(e) }

// Scan reads events until the peer closes the connection or fn returns an error,
// calling fn for each. A clean peer close (io.EOF) yields nil; fn's error (or a
// malformed-line / read error) is returned otherwise.
func (k *Conn) Scan(fn func(contracts.Event) error) error {
	return ScanEvents(k.c, fn)
}

// Close closes the underlying connection.
func (k *Conn) Close() error { return k.c.Close() }

// Acceptor listens on a control socket and yields one persistent Conn per
// dialing bridge. Unlike the one-shot Server (single pick per connection) it
// keeps each connection open for the session's life.
type Acceptor struct {
	ln    net.Listener
	conns chan *Conn
}

// Accept binds the control socket at path (removing a stale socket first) and
// starts accepting bridge connections. Each appears on Conns().
func Accept(path string) (*Acceptor, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	a := &Acceptor{ln: ln, conns: make(chan *Conn, 1)}
	go a.loop()
	return a, nil
}

func (a *Acceptor) loop() {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			close(a.conns)
			return // listener closed
		}
		a.conns <- newConn(c)
	}
}

// Conns yields each bridge connection; it closes when the Acceptor is closed.
func (a *Acceptor) Conns() <-chan *Conn { return a.conns }

// Close stops listening and removes the socket file.
func (a *Acceptor) Close() error {
	err := a.ln.Close()
	_ = os.Remove(a.ln.Addr().String())
	return err
}

// Dial connects to the hub's control socket, returning a persistent Conn. The
// bridge calls this at startup and again on each reconnect.
func Dial(path string) (*Conn, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return newConn(c), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./core/internal/control/`
Expected: PASS (round-trip + concurrent-writes).

- [ ] **Step 5: Commit**

```bash
git add core/internal/control/conn.go core/internal/control/conn_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(control): persistent bidirectional Conn (Dial/Accept)"
```

### Task 1.2: reconnect semantics test

**Files:**
- Modify: `core/internal/control/conn_test.go`

- [ ] **Step 1: Write the failing test** (append to `conn_test.go`)

```go
func TestAcceptorYieldsSecondConnAfterFirstCloses(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rc.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()

	c1, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial 1: %v", err)
	}
	<-acc.Conns()
	c1.Close() // simulate a bridge crash

	c2, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial 2 (reconnect): %v", err)
	}
	defer c2.Close()
	select {
	case sc := <-acc.Conns():
		if sc == nil {
			t.Fatal("second conn is nil")
		}
	case <-time.After(time.Second):
		t.Fatal("acceptor did not yield a second conn after reconnect")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./core/internal/control/ -run TestAcceptorYieldsSecond`
Expected: PASS (the implementation already supports it — this test pins the reconnect contract M2/M4 rely on).

- [ ] **Step 3: Commit**

```bash
git add core/internal/control/conn_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "test(control): pin acceptor reconnect contract"
```

---

## Milestone 2 — bridge runner mode (`--hub-socket`)

**Why:** in pure-runner topology the bridge does no gateway I/O. M2 adds an alternate `Run` path, selected by `Options.HubSocket`, that dials the hub, reads `input`/`pick` frames, runs the backend, and emits events back. The existing Discord-polling path is **untouched** (selected when `HubSocket == ""`), so master stays green; that old path is deleted in M5.

### Task 2.1: `runHub` — the runner-mode loop

**Files:**
- Create: `core/bridge/hub.go`
- Test: `core/bridge/hub_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/bridge/hub_test.go`:

```go
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

func TestRunHubOneTurn(t *testing.T) {
	sink := &recordSink{}
	in := make(chan contracts.Event, 2)
	in <- contracts.Event{T: "input", Who: "alice", Text: "refactor"}
	close(in)

	be := fakeBackend{reply: "done · 4 files"}
	runHubTurns(context.Background(), in, sink, be, nil, Options{})

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

	runHubTurns(context.Background(), in, sink, fakeBackend{reply: ""}, nil, Options{})

	if len(sink.events) == 0 || sink.events[len(sink.events)-1] != (contracts.Event{T: "reply", Done: true}) {
		t.Fatalf("empty reply must still emit a terminal reply{done}; got %+v", sink.events)
	}
}

func TestRunHubContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := make(chan contracts.Event) // never fed
	done := make(chan struct{})
	go func() { runHubTurns(ctx, in, &recordSink{}, fakeBackend{}, nil, Options{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runHubTurns did not return on cancelled context")
	}
}
```

(`fakeBackend` is the one already defined in `runner_test.go`: it emits one `text` event "thinking" then returns its reply.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./core/bridge/ -run TestRunHub`
Expected: FAIL — `undefined: runHubTurns`.

- [ ] **Step 3: Write the implementation**

Create `core/bridge/hub.go`:

```go
package bridge

import (
	"context"
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
)

// runHub is the pure-runner loop: it dials the hub control socket, reads input
// frames, and drives the backend one turn at a time, emitting events back over
// the same connection. It reconnects on connection loss; the supervisor's
// process-level restart is the outer backstop. ctx cancellation returns nil.
func runHub(ctx context.Context, newBackend BackendFactory, orch contracts.Orchestrator, o Options) error {
	resp, err := newBackend(o.Channel)
	if err != nil {
		return fmt.Errorf("backend: %w", err)
	}
	defer resp.Close()

	conn, err := control.Dial(o.HubSocket)
	if err != nil {
		return fmt.Errorf("dial hub socket %s: %w", o.HubSocket, err)
	}
	defer conn.Close()

	// The hub frames inputs as JSON-line Events; surface them on a channel the
	// turn driver consumes. Scan returns when the hub closes the conn (daemon
	// gone or session closed) → the bridge exits and the supervisor decides.
	in := make(chan contracts.Event)
	go func() {
		defer close(in)
		_ = conn.Scan(func(e contracts.Event) error {
			select {
			case in <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	runHubTurns(ctx, in, conn, resp, orch, o)
	return ctx.Err()
}

// runHubTurns serially drains input frames, running one backend turn per
// input/pick. It is split from runHub so it can be unit-tested over an
// in-memory channel + sink without a real socket. FIFO is inherent: the hub
// sends the next input only after it sees this turn's reply{done}, and this
// loop processes one frame at a time anyway.
func runHubTurns(ctx context.Context, in <-chan contracts.Event, sink contracts.EventSink, resp contracts.Backend, orch contracts.Orchestrator, o Options) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			switch ev.T {
			case "pick":
				runPick(ctx, sink, resp, ev.Value, o)
			default: // "input" (and any human-origin frame)
				runOneTurn(ctx, sink, resp, orch, ev, o)
			}
		}
	}
}

// runOneTurn runs a single backend turn for an input frame, streaming chunk/
// status events and a terminal reply{done}. An empty output still emits
// reply{done} so the hub's FIFO can advance.
func runOneTurn(ctx context.Context, sink contracts.EventSink, resp contracts.Backend, orch contracts.Orchestrator, ev contracts.Event, o Options) {
	var memCtx string
	if orch != nil {
		memCtx = orch.Context(ctx)
	}
	prompt := contracts.Prompt{Content: ev.Text, Context: memCtx, Author: ev.Who}
	onEvent := func(be contracts.BackendEvent) { emitBackendEvent(sink, be) }
	out, err := resp.Respond(ctx, prompt, onEvent)
	if err != nil && out == "" {
		out = "⚠️ " + err.Error()
	}
	out = strings.TrimSpace(out)
	sink.Emit(contracts.Event{T: "reply", Text: out, Done: true})
	if orch != nil {
		_ = orch.Observe(ctx, prompt, out)
	}
}

// runPick answers a routed select-menu pick out-of-band (serialized with turns
// by runHubTurns), emitting whatever the backend produces as a reply{done}.
func runPick(ctx context.Context, sink contracts.EventSink, resp contracts.Backend, value string, o Options) {
	inj, ok := resp.(contracts.ChoiceInjector)
	if !ok {
		return
	}
	out, err := inj.InjectChoice(ctx, value)
	if err != nil {
		out = "⚠️ " + err.Error()
	}
	sink.Emit(contracts.Event{T: "reply", Text: strings.TrimSpace(out), Done: true})
}

// emitBackendEvent maps a backend progress event onto the bus vocabulary: text
// → chunk, tool → status (dropped when empty), reset → reset; others (result)
// carry no transcript and are dropped. Mirrors the relocated runner.emitBackend.
func emitBackendEvent(sink contracts.EventSink, be contracts.BackendEvent) {
	switch be.Kind {
	case "text":
		sink.Emit(contracts.Event{T: "chunk", Text: be.Detail})
	case "tool":
		if text := strings.TrimSpace(be.Tool + " " + be.Detail); text != "" {
			sink.Emit(contracts.Event{T: "status", Text: text})
		}
	case "reset":
		sink.Emit(contracts.Event{T: "reset"})
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./core/bridge/ -run TestRunHub`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/bridge/hub.go core/bridge/hub_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(bridge): pure-runner hub-mode turn loop (runHub)"
```

### Task 2.2: dispatch `Run` to hub mode; add `--hub-socket`

**Files:**
- Modify: `core/bridge/bridge.go:43-56` (Options) and `core/bridge/bridge.go:196-199` (Run head)
- Modify: `bridge.go:35` and `bridge.go:66-78` (root flag + pass-through)

- [ ] **Step 1: Add `HubSocket` to `Options`**

In `core/bridge/bridge.go`, inside `type Options struct`, add after `ControlSocket`:

```go
	// HubSocket, when set, selects pure-runner (hub) mode: the bridge dials this
	// socket, reads input/pick frames from the daemon hub, and emits turn events
	// back instead of polling a gateway. Empty selects the legacy polling path.
	HubSocket string
```

- [ ] **Step 2: Dispatch at the top of `Run`**

In `core/bridge/bridge.go`, make `Run` branch before the polling setup. Replace the body start of `Run` (right after the `Progress` validation block, before the `ch := o.Channel` line) — insert:

```go
	// Pure-runner mode: no gateway polling, no posting. The daemon hub owns all
	// gateway I/O; the bridge only runs the backend over the control socket.
	if o.HubSocket != "" {
		return runHub(ctx, newBackend, orch, o)
	}
```

Place it after `if o.Progress == "" { o.Progress = "full" }` and before the `// No channel configured` comment. (The `p.Enabled()` guard above it stays for the legacy path; in hub mode `p` is unused but still passed — acceptable until M5 deletes the legacy path. To avoid an "enabled" failure blocking hub mode, move the `if !p.Enabled()` check into the legacy branch: delete it from the top of `Run` and re-add it immediately after the hub-mode dispatch, i.e. just before `ch := o.Channel`.)

- [ ] **Step 3: Register the flag and pass it through (root `bridge.go`)**

In `bridge.go`, after the `controlSocket` flag (line 35), add:

```go
	hubSocket := fs.String("hub-socket", "", "unix socket of the daemon hub: when set, run as a pure backend runner (no gateway polling)")
```

In the `bridge.Run(...)` call's `bridge.Options{...}` literal, add:

```go
		HubSocket:     *hubSocket,
```

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: PASS — legacy path unchanged (existing `runner_test.go` still green), hub path covered by `hub_test.go`, purity green.

- [ ] **Step 5: Commit**

```bash
git add core/bridge/bridge.go bridge.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(bridge): --hub-socket selects pure-runner mode"
```

---

## Milestone 3 — terminal gateway plugin

**Why:** the terminal must be a gateway peer to Discord. It implements `contracts.Gateway` (so the hub can fan out), `contracts.ChannelReader` (so the hub polls typed lines via `Read`, exactly like Discord), and `contracts.EventSink` (so the hub streams the live event stream to the TUI). It self-registers via `init()` and exposes a package-level `Active()` for the TUI to bind to.

### Task 3.1: `Terminal` type + registration

**Files:**
- Create: `plugins/terminal/terminal.go`
- Test: `plugins/terminal/terminal_test.go`

- [ ] **Step 1: Write the failing test**

Create `plugins/terminal/terminal_test.go`:

```go
package terminal

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestReadDrainsSubmittedLines(t *testing.T) {
	tm := New()
	tm.Submit("hello")
	tm.Submit("world")

	msgs, err := tm.Read(context.Background(), "terminal", 100, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "world" {
		t.Fatalf("Read = %+v, want hello/world", msgs)
	}
	// A second Read drains nothing (already consumed).
	msgs, _ = tm.Read(context.Background(), "terminal", 100, "")
	if len(msgs) != 0 {
		t.Fatalf("second Read = %+v, want empty", msgs)
	}
}

func TestEmitForwardsToFrontend(t *testing.T) {
	tm := New()
	got := make([]contracts.Event, 0, 2)
	done := make(chan struct{})
	go func() {
		for e := range tm.Frontend() {
			got = append(got, e)
			if len(got) == 2 {
				close(done)
				return
			}
		}
	}()
	tm.Emit(contracts.Event{T: "chunk", Text: "a"})
	tm.Emit(contracts.Event{T: "reply", Text: "b", Done: true})
	<-done
	if got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("frontend got %+v, want a/b", got)
	}
}

func TestPostEmitsReplyEvent(t *testing.T) {
	tm := New()
	got := make(chan contracts.Event, 1)
	go func() { got <- <-tm.Frontend() }()
	if _, err := tm.Post(context.Background(), contracts.Conversation{Gateway: "terminal", ID: "terminal"}, "hi"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if e := <-got; e.T != "reply" || e.Text != "hi" {
		t.Fatalf("Post emitted %+v, want reply/hi", e)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./plugins/terminal/ -run TestRead`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Write the implementation**

Create `plugins/terminal/terminal.go`:

```go
// Package terminal is the terminal gateway plugin: a chat gateway whose
// "channel" is the local TUI. It self-registers like any gateway (init →
// contracts.Default) and implements Gateway + ChannelReader + EventSink so the
// daemon hub drives it exactly like Discord — polling Read for typed lines and
// fanning the live event stream to it via Emit. The TUI binds to the active
// instance through Active().
package terminal

import (
	"context"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// The fixed conversation id the terminal gateway uses (single local channel).
const ChannelID = "terminal"

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:         "terminal",
			Category:     contracts.CategoryGateway,
			Capabilities: contracts.Capabilities{Replies: true},
		},
		Gateway: newGatewaySet,
	})
}

// active holds the instance built by the factory so the TUI (constructed in the
// host's serve loop) can bind to the very gateway the hub drives. The daemon
// builds at most one terminal gateway per process.
var (
	activeMu sync.Mutex
	active   *Terminal
)

// Active returns the terminal gateway built for this process, or nil if the
// terminal gateway was not instantiated (e.g. no TTY / not registered).
func Active() *Terminal {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

func newGatewaySet(ctx context.Context, cfg contracts.PluginConfig) (contracts.GatewaySet, error) {
	tm := New()
	activeMu.Lock()
	active = tm
	activeMu.Unlock()
	return contracts.GatewaySet{Gateway: tm, Reader: tm}, nil
}

// Terminal is the in-process terminal gateway. Typed lines arrive via Submit and
// are drained by the hub through Read; outbound events (Emit/Post/Reply) are
// forwarded to the TUI on Frontend.
type Terminal struct {
	mu      sync.Mutex
	pending []contracts.Message
	nextID  int
	out     chan contracts.Event
}

// New builds an unbound terminal gateway.
func New() *Terminal {
	return &Terminal{out: make(chan contracts.Event, 64)}
}

// Submit enqueues a line the user typed in the TUI as an inbound message.
func (t *Terminal) Submit(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	t.pending = append(t.pending, contracts.Message{
		ID:         msgID(t.nextID),
		ChannelID:  ChannelID,
		Content:    text,
		AuthorID:   "local",
		AuthorName: "you",
	})
}

// Frontend yields outbound events for the TUI to render.
func (t *Terminal) Frontend() <-chan contracts.Event { return t.out }

// --- contracts.ChannelReader ---

func (t *Terminal) Enabled() bool          { return true }
func (t *Terminal) DefaultChannel() string { return ChannelID }
func (t *Terminal) EnsureChannel(context.Context, string, string) (contracts.Channel, error) {
	return contracts.Channel{ID: ChannelID, Name: ChannelID}, nil
}

// Read drains and returns all lines typed since the last Read (the hub polls
// this like any gateway). after/limit are ignored: the terminal has no history.
func (t *Terminal) Read(_ context.Context, _ string, _ int, _ string) ([]contracts.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) == 0 {
		return nil, nil
	}
	out := t.pending
	t.pending = nil
	return out, nil
}

func (t *Terminal) Unreact(context.Context, string, string, string) error { return nil }
func (t *Terminal) UpsertStatusMessage(_ context.Context, _, _, content string) (string, error) {
	t.emit(contracts.Event{T: "status", Text: content})
	return "", nil
}

// --- contracts.Gateway ---

func (t *Terminal) Manifest() contracts.Manifest {
	return contracts.Manifest{Kind: "terminal", Category: contracts.CategoryGateway}
}
func (t *Terminal) Post(_ context.Context, _ contracts.Conversation, text string) (contracts.MessageID, error) {
	t.emit(contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}
func (t *Terminal) Reply(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	t.emit(contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}
func (t *Terminal) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (t *Terminal) Menu(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, prompt string, opts []contracts.Choice) error {
	t.emit(contracts.Event{T: "status", Text: prompt})
	return nil
}

// --- contracts.EventSink ---

// Emit forwards a live turn event to the TUI. This is the rich path: when the
// hub sees the terminal gateway implements EventSink it streams every event
// here rather than only posting the final reply.
func (t *Terminal) Emit(e contracts.Event) { t.emit(e) }

func (t *Terminal) emit(e contracts.Event) {
	select {
	case t.out <- e:
	default: // TUI not draining fast enough → drop rather than block the hub
	}
}

func msgID(n int) string {
	return "t" + itoa(n)
}

// itoa avoids importing strconv for a single small positive int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./plugins/terminal/`
Expected: PASS.

- [ ] **Step 5: Verify purity is still green** (the plugin is outside `core/...` and is not imported by `core`)

Run: `go test ./... -run 'TestCorePurity|TestHostPurity'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugins/terminal/terminal.go plugins/terminal/terminal_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(terminal): terminal gateway plugin (Gateway+Reader+EventSink)"
```

### Task 3.2: blank-import the terminal plugin

**Files:**
- Modify: `plugins.go` (root)

- [ ] **Step 1: Inspect the current plugins file**

Run: `cat plugins.go` (read it to match the existing blank-import style).

- [ ] **Step 2: Add the blank import**

In `plugins.go`, add to the import block (alongside the existing gateway/backend imports):

```go
	_ "github.com/Herrscherd/herrscher/plugins/terminal"
```

- [ ] **Step 3: Verify the plugin is discovered**

Run: `go build ./... && go test ./... -run 'TestHostPurity'`
Expected: build OK, purity PASS (host importing an in-repo plugin is allowed; only `dctl` is forbidden in package main).

- [ ] **Step 4: Commit**

```bash
git add plugins.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(host): register terminal gateway plugin"
```

---

## Milestone 4 — hub turn-loop + Bubbletea TUI (MVP lights up)

**Why:** this is where the daemon becomes a hub and the TUI appears. The hub runs one `sessionDriver` per session: it polls every bound gateway's `Read`, enqueues inputs into a FIFO, writes them to the bridge over the accepted `Conn` one turn at a time, and fans each returned event out to all bound gateways (rich `EventSink.Emit` when available, else `Post` of the final reply). The TUI is the terminal gateway's frontend.

### Task 4.1: `sessionDriver` — FIFO + pump + fan-out

**Files:**
- Create: `core/host/turnloop.go`
- Test: `core/host/turnloop_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/host/turnloop_test.go`:

```go
package host

import (
	"context"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// fanRecorder is a gateway+reader+sink that records what the hub fans to it and
// can feed inbound lines.
type fanRecorder struct {
	mu       sync.Mutex
	inbound  []contracts.Message
	emitted  []contracts.Event
	posted   []string
	sink     bool // implements EventSink when true
}

func (f *fanRecorder) feed(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = append(f.inbound, contracts.Message{ID: "m", ChannelID: "c", Content: text, AuthorName: "you"})
}
func (f *fanRecorder) Enabled() bool          { return true }
func (f *fanRecorder) DefaultChannel() string { return "c" }
func (f *fanRecorder) EnsureChannel(context.Context, string, string) (contracts.Channel, error) {
	return contracts.Channel{ID: "c"}, nil
}
func (f *fanRecorder) Read(context.Context, string, int, string) ([]contracts.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.inbound
	f.inbound = nil
	return out, nil
}
func (f *fanRecorder) Unreact(context.Context, string, string, string) error { return nil }
func (f *fanRecorder) UpsertStatusMessage(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fanRecorder) Manifest() contracts.Manifest { return contracts.Manifest{Kind: "rec"} }
func (f *fanRecorder) Post(_ context.Context, _ contracts.Conversation, text string) (contracts.MessageID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, text)
	return "", nil
}
func (f *fanRecorder) Reply(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	return f.Post(nil, contracts.Conversation{}, text)
}
func (f *fanRecorder) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (f *fanRecorder) Menu(context.Context, contracts.Conversation, contracts.MessageID, string, []contracts.Choice) error {
	return nil
}

// fakeBridge is the bridge half of a turn: it reads input frames the driver
// writes and replies with chunk+reply{done}.
type fakeBridge struct {
	inputs  chan contracts.Event
	replies func(contracts.Event) []contracts.Event
}

func TestDriverFanOutToAllBoundGateways(t *testing.T) {
	a := &fanRecorder{}
	b := &fanRecorder{}
	a.feed("hello")

	// A pair of in-memory event streams stands in for the Conn: toBridge carries
	// inputs, fromBridge carries the bridge's replies.
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1",
		[]contracts.GatewaySet{{Gateway: a, Reader: a}, {Gateway: b, Reader: b}},
		toBridge, fromBridge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// The driver should poll a.Read, see "hello", and write an input frame.
	select {
	case in := <-toBridge:
		if in.T != "input" || in.Text != "hello" {
			t.Fatalf("driver wrote %+v, want input/hello", in)
		}
	case <-time.After(time.Second):
		t.Fatal("driver did not pump input to bridge")
	}
	// Simulate the bridge replying; the driver fans it to BOTH gateways.
	fromBridge <- contracts.Event{T: "reply", Text: "world", Done: true}

	waitFor(t, func() bool {
		a.mu.Lock(); b.mu.Lock(); defer a.mu.Unlock(); defer b.mu.Unlock()
		return len(a.posted) == 1 && a.posted[0] == "world" && len(b.posted) == 1 && b.posted[0] == "world"
	}, "reply fanned to both gateways")
}

func TestDriverFIFOSerializesTurns(t *testing.T) {
	a := &fanRecorder{}
	a.feed("first")
	a.feed("second")
	toBridge := make(chan contracts.Event, 8)
	fromBridge := make(chan contracts.Event, 8)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// First input pumped; second must NOT appear until we reply{done} to the first.
	if got := <-toBridge; got.Text != "first" {
		t.Fatalf("first frame = %+v", got)
	}
	select {
	case got := <-toBridge:
		t.Fatalf("second frame %+v pumped before first turn completed", got)
	case <-time.After(150 * time.Millisecond):
	}
	fromBridge <- contracts.Event{T: "reply", Text: "r1", Done: true}
	if got := <-toBridge; got.Text != "second" {
		t.Fatalf("second frame = %+v, want second after reply{done}", got)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./core/host/ -run TestDriver`
Expected: FAIL — `undefined: newSessionDriver`.

- [ ] **Step 3: Write the implementation**

Create `core/host/turnloop.go`:

```go
package host

import (
	"context"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// pollInterval is how often the driver polls each bound gateway's Read for new
// inbound lines. Matches the bridge's historical default cadence.
const pollInterval = time.Second

// sessionDriver owns one session's turn lifecycle: it polls every bound
// gateway's Reader for inbound messages, serializes them through a FIFO, writes
// one input frame to the bridge per turn, and fans the bridge's reply events out
// to all bound gateways. toBridge/fromBridge are the two directions of the
// session's control connection (a *control.Conn in production; channels in
// tests).
type sessionDriver struct {
	name     string
	gateways []contracts.GatewaySet
	toBridge chan<- contracts.Event
	from     <-chan contracts.Event
	queue    chan contracts.Event
}

func newSessionDriver(name string, gws []contracts.GatewaySet, toBridge chan<- contracts.Event, fromBridge <-chan contracts.Event) *sessionDriver {
	return &sessionDriver{
		name:     name,
		gateways: gws,
		toBridge: toBridge,
		from:     fromBridge,
		queue:    make(chan contracts.Event, 64),
	}
}

// run starts the pollers and the turn pump; it blocks until ctx is cancelled.
func (d *sessionDriver) run(ctx context.Context) {
	for _, g := range d.gateways {
		if g.Reader != nil {
			go d.poll(ctx, g.Reader)
		}
	}
	d.pump(ctx)
}

// poll reads one gateway's inbound messages and enqueues them as input frames.
func (d *sessionDriver) poll(ctx context.Context, r contracts.ChannelReader) {
	ch := r.DefaultChannel()
	var last string
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := r.Read(ctx, ch, 100, last)
		if err == nil {
			for _, m := range msgs {
				if m.AuthorBot {
					continue
				}
				last = m.ID
				select {
				case d.queue <- contracts.Event{T: "input", Who: m.AuthorName, Text: m.Content}:
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// pump dequeues one input at a time, writes it to the bridge, then blocks
// fanning the bridge's events out until that turn's reply{done} — this is the
// FIFO serialization. A nil event from `from` (channel closed → bridge gone)
// abandons the in-flight turn; the next queued input is sent once a new
// connection is wired by the caller (Hub.run swaps `from` on reconnect).
func (d *sessionDriver) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-d.queue:
			select {
			case d.toBridge <- ev:
			case <-ctx.Done():
				return
			}
			d.awaitTurn(ctx)
		}
	}
}

// awaitTurn fans every event for the current turn to all bound gateways and
// returns when it sees reply{done} (or ctx is cancelled / the bridge closed).
func (d *sessionDriver) awaitTurn(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-d.from:
			if !ok {
				return // bridge connection lost; abandon this turn
			}
			d.fanOut(ctx, e)
			if e.T == "reply" && e.Done {
				return
			}
		}
	}
}

// fanOut delivers one turn event to every bound gateway: a gateway implementing
// EventSink renders the full stream itself; otherwise only the final reply is
// posted through the Gateway port (the host renderer added in M5 enriches the
// non-EventSink path).
func (d *sessionDriver) fanOut(ctx context.Context, e contracts.Event) {
	conv := contracts.Conversation{ID: e.Who} // placeholder; real conv set per gateway in M5
	for _, g := range d.gateways {
		if sink, ok := g.Gateway.(contracts.EventSink); ok {
			sink.Emit(e)
			continue
		}
		if e.T == "reply" && e.Done && e.Text != "" {
			_, _ = g.Gateway.Post(ctx, contracts.Conversation{Gateway: g.Gateway.Manifest().Kind, ID: gatewayChannel(g)}, e.Text)
		}
	}
	_ = conv
}

// gatewayChannel returns the default channel for a gateway set, or "" when it
// has no reader.
func gatewayChannel(g contracts.GatewaySet) string {
	if g.Reader != nil {
		return g.Reader.DefaultChannel()
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./core/host/ -run TestDriver`
Expected: PASS (fan-out to both gateways; FIFO holds the second turn).

- [ ] **Step 5: Verify purity (core/host imports only contracts + stdlib here)**

Run: `go test ./... -run TestCorePurity`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add core/host/turnloop.go core/host/turnloop_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(host): per-session FIFO turn driver with multi-gateway fan-out"
```

### Task 4.2: `Hub.run` — bind the driver to a real `Conn` with reconnect

**Files:**
- Create: append to `core/host/turnloop.go`
- Test: append to `core/host/turnloop_test.go`

- [ ] **Step 1: Write the failing test**

Append to `core/host/turnloop_test.go`:

```go
func TestRunSessionReconnectsAndResumes(t *testing.T) {
	a := &fanRecorder{}
	a.feed("q1")
	sock := tmpSock(t)
	acc, err := acceptCtl(sock)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer acc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunSession(ctx, "s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, acc)

	// First bridge connects, gets the input, then dies before replying.
	c1 := dialCtl(t, sock)
	gotInput(t, c1, "q1")
	c1.Close()

	// A new bridge connects (supervisor restart). The driver should resume and
	// the next queued input flows. Feed a second line and expect it.
	a.feed("q2")
	c2 := dialCtl(t, sock)
	defer c2.Close()
	gotInput(t, c2, "q2")
}
```

Plus these test helpers (append; they wrap the real `control` package so the test exercises the production transport):

```go
func tmpSock(t *testing.T) string { t.Helper(); return filepathJoin(t.TempDir(), "h.sock") }
```

> **Note for the implementer:** import `control "github.com/Herrscherd/herrscher/core/internal/control"` and `path/filepath` in the test; implement `acceptCtl`/`dialCtl`/`gotInput`/`filepathJoin` as thin wrappers: `acceptCtl = control.Accept`, `dialCtl` calls `control.Dial`, `gotInput` does a `Scan` with a 1s timeout asserting the first `input` frame's Text, `filepathJoin = filepath.Join`. Keep `RunSession`'s signature as written below.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./core/host/ -run TestRunSessionReconnects`
Expected: FAIL — `undefined: RunSession`.

- [ ] **Step 3: Write the implementation** (append to `core/host/turnloop.go`)

```go
import (
	// add to the existing import block:
	"github.com/Herrscherd/herrscher/core/internal/control"
)

// RunSession drives one session against a control Acceptor: it bridges the
// persistent Conn (input frames out, event frames in) to a sessionDriver, and
// re-binds to a fresh Conn whenever the bridge reconnects (after a crash +
// supervisor restart). It blocks until ctx is cancelled.
func RunSession(ctx context.Context, name string, gws []contracts.GatewaySet, acc *control.Acceptor) {
	toBridge := make(chan contracts.Event)
	fromBridge := make(chan contracts.Event)
	d := newSessionDriver(name, gws, toBridge, fromBridge)
	go d.run(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case conn, ok := <-acc.Conns():
			if !ok {
				return
			}
			serveConn(ctx, conn, toBridge, fromBridge)
		}
	}
}

// serveConn shuttles frames between the driver and one bridge connection until
// the connection closes or ctx is cancelled. The reader goroutine forwards the
// bridge's events into fromBridge; the writer drains toBridge to the conn.
func serveConn(ctx context.Context, conn *control.Conn, toBridge <-chan contracts.Event, fromBridge chan<- contracts.Event) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()

	go func() {
		_ = conn.Scan(func(e contracts.Event) error {
			select {
			case fromBridge <- e:
				return nil
			case <-cctx.Done():
				return cctx.Err()
			}
		})
		cancel() // connection closed → unblock the writer and return to re-accept
	}()

	for {
		select {
		case <-cctx.Done():
			return
		case ev := <-toBridge:
			if err := conn.Write(ev); err != nil {
				return // write failed → connection dead, go re-accept
			}
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./core/host/ -run TestRunSessionReconnects`
Expected: PASS.

- [ ] **Step 5: Full suite + purity**

Run: `go test ./...`
Expected: PASS — `core/host` importing `core/internal/control` is allowed (both are core; purity only forbids dctl/plugins).

- [ ] **Step 6: Commit**

```bash
git add core/host/turnloop.go core/host/turnloop_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(host): RunSession binds turn driver to control Conn with reconnect"
```

### Task 4.3: supervisor passes `--hub-socket`

**Files:**
- Modify: `core/internal/supervisor/supervisor.go:23-33`

- [ ] **Step 1: Write the failing test**

Create `core/internal/supervisor/supervisor_test.go` (if it doesn't exist; otherwise append):

```go
package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher/core/internal/state"
)

func TestBridgeArgsIncludesHubSocket(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/herrscher")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Cmd: "claude"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--hub-socket") {
		t.Fatalf("bridgeArgs missing --hub-socket: %v", args)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./core/internal/supervisor/ -run TestBridgeArgsIncludesHubSocket`
Expected: FAIL — flag absent.

- [ ] **Step 3: Implement** — in `bridgeArgs`, add the hub socket (import `control`):

```go
import (
	// add:
	"github.com/Herrscherd/herrscher/core/internal/control"
)

func (s *Supervisor) bridgeArgs(sess state.Session) []string {
	args := []string{"bridge", "-c", sess.ChannelID, "--cmd", sess.Cmd, "--session", sess.Name,
		"--hub-socket", control.SocketPath(sess.Name)}
	if sess.Backend != "" && sess.Backend != "stream" {
		args = append(args, "--backend", sess.Backend)
	}
	if s.PartDir != "" {
		args = append(args, "--participants", state.ParticipantsPath(s.PartDir, sess.Name))
	}
	return args
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./core/internal/supervisor/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/internal/supervisor/supervisor.go core/internal/supervisor/supervisor_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(supervisor): pass --hub-socket to each bridge"
```

### Task 4.4: Bubbletea TUI

**Files:**
- Create: `plugins/terminal/tui/tui.go`
- Modify: `go.mod` (add Bubbletea deps)

- [ ] **Step 1: Add the dependencies**

Run:
```bash
cd /home/shan/dev/herrscher
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/charmbracelet/bubbles@latest
```
Expected: `go.mod` gains the three `charmbracelet` requires.

- [ ] **Step 2: Write the TUI model**

Create `plugins/terminal/tui/tui.go`:

```go
// Package tui renders the terminal gateway's live event stream and captures the
// operator's input, driving the active terminal gateway. It is the gateway's
// frontend: the daemon hub treats the terminal gateway like any other; the TUI
// is what makes that gateway a human-usable pane.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/plugins/terminal"
)

var (
	humanStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	statusStyle = lipgloss.NewStyle().Faint(true)
	replyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// eventMsg wraps a gateway event for the Bubbletea update loop.
type eventMsg contracts.Event

type model struct {
	tm     *terminal.Terminal
	vp     viewport.Model
	input  textinput.Model
	lines  []string
	ready  bool
}

// Run starts the TUI bound to the active terminal gateway, blocking until the
// user quits; quitting cancels ctx (wired by the caller) so the daemon shuts
// down cleanly. Returns nil if no terminal gateway was instantiated.
func Run(ctx context.Context, cancel context.CancelFunc) error {
	tm := terminal.Active()
	if tm == nil {
		return nil
	}
	in := textinput.New()
	in.Placeholder = "type a message…"
	in.Focus()
	m := model{tm: tm, input: in}
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Forward gateway events into the program.
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
	cancel() // quitting the TUI tears the daemon down
	return err
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-3)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 3
		}
		m.input.Width = msg.Width - 2
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text != "" {
				m.tm.Submit(text)
				m.append(humanStyle.Render("you ") + text)
				m.input.Reset()
			}
		}
	case eventMsg:
		m.renderEvent(contracts.Event(msg))
	}
	var cmds []tea.Cmd
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.vp, c = m.vp.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

func (m *model) renderEvent(e contracts.Event) {
	switch e.T {
	case "chunk":
		m.append(e.Text)
	case "status":
		m.append(statusStyle.Render("· " + e.Text))
	case "reply":
		if e.Text != "" {
			m.append(replyStyle.Render(e.Text))
		}
	case "reset":
		m.append(statusStyle.Render("· (turn reset)"))
	}
}

func (m *model) append(line string) {
	m.lines = append(m.lines, line)
	if m.ready {
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()
	}
}

func (m model) View() string {
	if !m.ready {
		return "starting…"
	}
	return fmt.Sprintf("%s\n%s", m.vp.View(), m.input.View())
}
```

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./...`
Expected: build OK (the TUI is not unit-tested — Bubbletea programs need a TTY; it is exercised manually in Step 5 of Task 4.5).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum plugins/terminal/tui/tui.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(terminal): Bubbletea TUI frontend for the terminal gateway"
```

### Task 4.5: wire the hub into `serve` (TTY-gated)

**Files:**
- Modify: `core/host/serve.go:140-191` (Run)
- Modify: `serve.go` (root) — TTY detection + TUI

- [ ] **Step 1: Replace the daemon's bare wait with per-session hub drivers**

In `core/host/serve.go`, the daemon currently starts a `supervisor` and blocks on `<-ctx.Done()`. Add, alongside the supervisor, one `Acceptor` + `RunSession` per session. Change the `Deps` the daemon receives so it carries **all** bound gateway sets, not just one. Introduce a new exported entrypoint that the root `serve.go` calls with the full gateway list:

Add to `core/host/serve.go`:

```go
import (
	// add:
	"github.com/Herrscherd/herrscher/core/internal/control"
)

// RunHub is the multi-gateway daemon: it supervises one pure-runner bridge per
// session and drives each session's turns over a control Acceptor, fanning
// events out to every bound gateway. gws are the gateway sets the daemon owns
// (built from the registry by the caller); the first with a Reader seeds the
// status loop. It supersedes Run for the pure-runner topology.
func RunHub(ctx context.Context, gws []Deps, o Options) error {
	st, err := state.LoadState(o.StatePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	var home *state.HomeRef
	if o.Home != nil {
		home = &state.HomeRef{ID: o.Home.ID, Type: o.Home.Type}
	}
	st.ApplyDefaults(home, o.Workspace, o.Source)

	self, _ := os.Executable()
	partDir := filepath.Dir(o.StatePath)
	sup := supervisor.NewSupervisor(ctx, self)
	sup.PartDir = partDir

	for _, sess := range st.SnapshotSessions() {
		acc, err := control.Accept(control.SocketPath(sess.Name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "dctl serve: session %q: control socket: %v\n", sess.Name, err)
			continue
		}
		bound := boundGateways(gws, sess.BoundGateways())
		go RunSession(ctx, sess.Name, bound, acc)
		_ = sup.Start(sess)
	}

	for _, p := range contracts.Default.Plugins() {
		fmt.Fprintf(os.Stderr, "dctl serve: plugin %s (%s)\n", p.Manifest.Kind, p.Manifest.Category)
	}
	fmt.Fprintln(os.Stderr, "dctl serve: hub up; supervising sessions.")
	<-ctx.Done()
	return ctx.Err()
}

// boundGateways selects, from all built gateway sets, those whose kind is in the
// session's bound set (Session.BoundGateways()). A bound kind that wasn't built
// is skipped (its config was absent).
func boundGateways(all []Deps, kinds []string) []Deps {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []Deps
	for _, g := range all {
		if g.Gateway != nil && want[g.Gateway.Manifest().Kind] {
			out = append(out, g)
		}
	}
	return out
}
```

(Keep the legacy `Run` for now; it is removed in M6 cleanup. `RunHub` is what `serve.go` calls.)

- [ ] **Step 2: Build all gateways and gate the TUI on a TTY in root `serve.go`**

In `serve.go`, change `buildGateway` usage in `runServe` to build **all** gateways via the hub and pass them to `RunHub`; detect a TTY and, when present, run the TUI. Replace the tail of `runServe` (the `deps, err := buildGateway(ctx)` block and the `host.Run(...)` call) with:

```go
	import (
		// add to serve.go's import block:
		"golang.org/x/term"
		"github.com/Herrscherd/herrscher/core/host"
		"github.com/Herrscherd/herrscher/plugins/terminal/tui"
	)

	hub, err := host.BuildHub(ctx, contracts.Default.Gateways(), os.Getenv)
	if err != nil {
		return err
	}
	var gws []host.Deps
	for _, kind := range hub.Kinds() {
		if set, ok := hub.Get(kind); ok {
			set.Gateway = contracts.Degrade(set.Gateway)
			gws = append(gws, set)
		}
	}

	opts := host.Options{
		StatePath:     *statePath,
		DefaultCmd:    *defaultCmd,
		HealthAddr:    *healthAddr,
		StatusChannel: *statusChannel,
		InstanceID:    *instanceID,
		Owner:         owner,
		Home:          home,
		Workspace:     cfg.Workspace,
		Source:        cfg.Source,
	}

	// Foreground + interactive TTY → run the TUI as the terminal gateway's
	// frontend; quitting it cancels ctx and stops the daemon. Background service
	// (no TTY) → headless, terminal gateway absent, only remote gateways run.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = host.RunHub(ctx, gws, opts) }()
		return tui.Run(ctx, cancel)
	}
	return host.RunHub(ctx, gws, opts)
```

> The terminal gateway only participates when its plugin is registered AND a session binds it. For the MVP, ensure at least one session binds `["terminal"]` (created via `session create --terminal_only`, already supported by `ParseGateways`). The TUI shows nothing useful until such a session exists.

- [ ] **Step 3: Add the `term` dependency**

Run: `go get golang.org/x/term@latest`
Expected: `go.mod` gains `golang.org/x/term`.

- [ ] **Step 4: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: build OK; all tests + purity green.

- [ ] **Step 5: Manual smoke (MVP acceptance)**

Run (in a real terminal):
```bash
go build -o /tmp/herrscher . && \
  DCTL_STATE_DIR=$(mktemp -d) /tmp/herrscher session create demo --terminal_only && \
  DCTL_STATE_DIR=<same dir> /tmp/herrscher serve
```
Expected: an alt-screen TUI opens; typing a line and pressing Enter shows `you <line>`, then streamed `· status` lines and a green reply from the backend; Ctrl-C exits cleanly. (If no Claude backend is configured, the reply is the backend's error line — the loop still completes.)

- [ ] **Step 6: Commit**

```bash
git add serve.go core/host/serve.go go.mod go.sum
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(serve): TTY-gated TUI + multi-gateway hub (RunHub)"
```

---

## Milestone 5 — re-attach Discord through the hub

**Why:** the terminal works end-to-end; now Discord must flow through the same hub so it is a true peer. Discord's rich rendering (progress view, threaded replies, ack/done reactions, choice menus) currently lives in `core/bridge` and uses **only** contracts ports — so it relocates into a host-side, gateway-agnostic `gatewayRenderer` driven by the event stream, used for any gateway lacking `EventSink`. The bridge's Discord-polling path is then deleted.

### Task 5.1: `gatewayRenderer` — agnostic event→gateway rendering

**Files:**
- Create: `core/host/renderer.go`
- Test: `core/host/renderer_test.go`
- Reference (relocate from): `core/bridge/bridge.go:309-356` (`postResult`/`postResultGW`/`chunk`) and `core/bridge/progress.go` (the `progressView` — read it first: `cat core/bridge/progress.go`).

- [ ] **Step 1: Write the failing test**

Create `core/host/renderer_test.go`:

```go
package host

import (
	"context"
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./core/host/ -run TestRenderer`
Expected: FAIL — `undefined: newGatewayRenderer`.

- [ ] **Step 3: Relocate the rendering helpers and write `gatewayRenderer`**

First read the source being relocated: `cat core/bridge/progress.go` and re-read `core/bridge/bridge.go:309-397`.

Create `core/host/renderer.go` containing: the `progressView` type (moved verbatim from `core/bridge/progress.go`, package changed to `host`), the `chunk` splitter (moved from `bridge.go`), and:

```go
package host

import (
	"context"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// gatewayMaxLen is the hard per-message limit for chunking (Discord's 2000).
const gatewayMaxLen = 2000

// gatewayRenderer reproduces, on the daemon side, the rich gateway rendering the
// bridge used to do inline: a live progress view fed by status/chunk events, a
// threaded final reply, and ack/done reactions. It is gateway-agnostic — it uses
// only the contracts Gateway/ChannelReader ports — so the hub wraps any gateway
// that does NOT implement EventSink in one of these.
type gatewayRenderer struct {
	gw       contracts.Gateway
	reader   contracts.ChannelReader
	ch       string
	progress string
	conv     contracts.Conversation
	pv       *progressView
	replyTo  string
}

func newGatewayRenderer(gw contracts.Gateway, reader contracts.ChannelReader, ch, progress string) *gatewayRenderer {
	if progress == "" {
		progress = "full"
	}
	return &gatewayRenderer{
		gw:       gw,
		reader:   reader,
		ch:       ch,
		progress: progress,
		conv:     contracts.Conversation{Gateway: gw.Manifest().Kind, ID: ch},
	}
}

// handle renders one turn event onto the gateway. human starts a turn (opens a
// progress view); status/chunk feed it; reply finishes it and posts the result.
func (r *gatewayRenderer) handle(ctx context.Context, e contracts.Event) {
	switch e.T {
	case "human":
		if r.progress != "off" && r.reader != nil {
			post := func(id, content string) (string, error) {
				return r.reader.UpsertStatusMessage(ctx, r.ch, id, content)
			}
			r.pv = newProgressView(post, r.progress, false, nowFunc())
		}
	case "status":
		if r.pv != nil {
			r.pv.add(contracts.BackendEvent{Kind: "tool", Detail: e.Text})
		}
	case "chunk":
		if r.pv != nil {
			r.pv.add(contracts.BackendEvent{Kind: "text", Detail: e.Text})
		}
	case "reset":
		if r.pv != nil {
			r.pv.finish(true)
			r.pv = nil
		}
	case "reply":
		if e.Done {
			if e.Text != "" {
				for _, part := range chunk(e.Text, gatewayMaxLen) {
					_, _ = r.gw.Post(ctx, r.conv, part)
				}
			}
			if r.pv != nil {
				r.pv.finish(false)
				r.pv = nil
			}
		}
	}
}
```

> **Note on `nowFunc`/progressView:** `newProgressView` takes a `time.Time`; add an unexported `var nowFunc = time.Now` in `renderer.go` so tests are deterministic if needed. Move `progress.go`'s `newProgressView`, `progressView`, and its methods into `renderer.go` (or a sibling `progress.go` in `core/host`), changing only the package clause. Delete `core/bridge/progress.go` and its now-unused references in `bridge.go` in Task 5.3.

- [ ] **Step 4: Wire the renderer into fan-out**

In `core/host/turnloop.go`, replace `fanOut` so non-`EventSink` gateways are driven by a persistent `gatewayRenderer` (one per gateway per driver), not a one-off Post:

```go
// add a field to sessionDriver:
//   renderers map[string]*gatewayRenderer
// initialize in newSessionDriver: renderers: map[string]*gatewayRenderer{}

func (d *sessionDriver) fanOut(ctx context.Context, e contracts.Event) {
	for _, g := range d.gateways {
		if sink, ok := g.Gateway.(contracts.EventSink); ok {
			sink.Emit(e)
			continue
		}
		kind := g.Gateway.Manifest().Kind
		r := d.renderers[kind]
		if r == nil {
			r = newGatewayRenderer(g.Gateway, g.Reader, gatewayChannel(g), "full")
			d.renderers[kind] = r
		}
		r.handle(ctx, e)
	}
}
```

Remove the now-obsolete `conv`/placeholder code from the old `fanOut`.

- [ ] **Step 5: Run the tests**

Run: `go test ./core/host/`
Expected: PASS (renderer tests + existing driver tests; the driver tests use `fanRecorder` which has no `EventSink`, so they now exercise the renderer — `reply{done}` still posts "world"/"r1", matching the assertions).

- [ ] **Step 6: Commit**

```bash
git add core/host/renderer.go core/host/renderer_test.go core/host/turnloop.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "feat(host): agnostic gatewayRenderer for non-EventSink gateways"
```

### Task 5.2: choice-menu picks over the hub

**Files:**
- Modify: `core/host/renderer.go`
- Reference: `core/bridge/bridge.go:315-339` (`postResult` menu branch) and the manager's pick routing.

- [ ] **Step 1: Reproduce the menu branch in the renderer**

The bridge previously posted a select menu via `MenuRouter.RouteMenu` when the backend had a `PendingChoice`. In pure-runner the backend lives in the bridge, so the bridge must signal a pending choice over the bus. **Scope decision for this milestone:** carry the pending choice as a `status` event prefixed `choice: <question>` is insufficient for native menus. Instead, defer native select-menu rendering to a follow-up and have the renderer post the choice as plain text with numbered options (the numeric-reply fallback the bridge already supports). Document this explicitly:

Add to `gatewayRenderer.handle`, no new event type needed — the bridge already collapses an unanswered choice into its reply text in oneshot/fallback mode. Confirm by reading the claude backend's behavior: `grep -rn PendingChoice /home/shan/go/pkg/mod/github.com/\!herrscherd/herrscher-claude-backend@*/`. If the backend emits the choice as reply text, no renderer change is needed and native menus become a documented follow-up.

- [ ] **Step 2: Add a regression test that a numbered choice reply posts as text**

Append to `core/host/renderer_test.go`:

```go
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
```

(Add `"strings"` to the test imports.)

- [ ] **Step 3: Run + commit**

Run: `go test ./core/host/`
Expected: PASS.

```bash
git add core/host/renderer_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "test(host): choice reply renders as text (native menus = follow-up)"
```

> **Documented follow-up (out of scope):** native select-menu picks routed back over the bus (`pick` frames from gateway → bridge `InjectChoice`). The transport (`Conn` carries `pick` already; `runHub` handles `pick`) and the supervisor wiring are in place; only the daemon-side menu *emission* + click→`pick` routing is deferred.

### Task 5.3: delete the bridge's Discord-polling path

**Files:**
- Modify: `core/bridge/bridge.go` (remove `runner`, `handle`, the polling loop in `Run`, `postResult*`, `progress.go`)
- Modify: `bridge.go` (root) — `Run` no longer needs the gateway set for polling
- Modify: `core/bridge/runner_test.go` — delete (its subject is gone)

- [ ] **Step 1: Reduce `Run` to hub mode**

In `core/bridge/bridge.go`, replace the whole `Run` function with the hub-only version:

```go
// Run is the bridge entry point: a pure backend runner. It requires a hub socket
// (the daemon hub owns all gateway I/O) and drives the backend over it. The
// p/gw/sink parameters of the pre-pure-runner signature are gone.
func Run(ctx context.Context, newBackend BackendFactory, orch contracts.Orchestrator, o Options) error {
	switch o.Progress {
	case "", "off", "actions", "full":
	default:
		return fmt.Errorf("invalid --progress %q (want off|actions|full)", o.Progress)
	}
	if o.HubSocket == "" {
		return fmt.Errorf("bridge requires --hub-socket (pure-runner mode)")
	}
	return runHub(ctx, newBackend, orch, o)
}
```

Delete: the `runner` struct, `(*runner) emit`, `(*runner) emitBackend`, `(*runner) handle`, `postResult`, `postResultGW`, `removeFiles` (if unused elsewhere — check), `recordParticipant`, `oneline`, and the `discordMaxLen`/reaction consts if now unused. Delete `core/bridge/progress.go` (relocated to host). Delete `core/bridge/runner_test.go`. Keep `persist`/`chunk` only if still referenced — `chunk` moved to host, so delete it from bridge; `persist` is unused in hub mode, delete it. Keep `logf`, `ErrDisabled` only if referenced (remove `ErrDisabled` if not).

> Run `go build ./core/bridge/` iteratively and delete dead symbols the compiler flags as unused, or `grep` each symbol before removing.

- [ ] **Step 2: Update the root `bridge.go` call site**

In `bridge.go`, the bridge no longer builds a gateway for polling. Replace the `set, err := buildGateway(ctx)` block and the `bridge.Run(...)` call with:

```go
	mem := buildMemory(ctx, *verbose)
	if mem != nil {
		defer mem.Close()
	}
	orch := buildOrchestrator(ctx, mem, *session, *verbose)
	if orch != nil {
		defer orch.Close()
	}
	return bridge.Run(ctx, newBackend, orch, bridge.Options{
		Channel:      *ch,
		Interval:     *interval,
		State:        *state,
		Session:      *session,
		Verbose:      *verbose,
		Progress:     *progress,
		ProgressKeep: *progressKeep,
		HubSocket:    *hubSocket,
	})
```

Remove now-unused option fields from `Options` (`Ensure`, `After`, `Participants`, `ControlSocket`) **only if** nothing else references them — `ControlSocket` is still used by the old one-shot path until M6; defer its removal to M6. Keep `Ensure`/`After`/`Participants` removal to M6 cleanup to keep this task focused; leaving unused fields compiles fine.

- [ ] **Step 3: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: build OK; tests green. `TestHandleEmitsTurnEvents`/`TestEmitBackendMapping` are gone (their file was deleted); `hub_test.go` covers the runner. Purity green.

- [ ] **Step 4: Manual smoke — Discord through the hub**

With a `DISCORD_BOT_TOKEN` configured and a session bound to `["discord"]`, run `serve` headless (no TTY, e.g. piped) and post a message in the Discord channel; expect the ack reaction, a progress status message, the threaded reply, and the done reaction — identical to pre-Phase-3 behavior.

- [ ] **Step 5: Commit**

```bash
git add core/bridge/bridge.go bridge.go
git rm core/bridge/progress.go core/bridge/runner_test.go
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "refactor(bridge): pure-runner only; relocate rendering to host hub"
```

---

## Milestone 6 — cleanup + finalize contracts

### Task 6.1: route menu picks over the persistent Conn; delete one-shot pick path

**Files:**
- Modify: the manager pick-routing (find it: `grep -rn 'control.Send' --include='*.go' .`)
- Modify: `core/internal/control/control.go` (delete `Server`/`Listen`/`Send`/`accept`/`Values`/`Close` once no caller remains)
- Modify: `core/bridge/bridge.go` (delete the old `control.Listen` ChoiceInjector block — already gone with the `handle`/`Run` rewrite in M5; verify)

- [ ] **Step 1: Find remaining callers of the one-shot API**

Run: `grep -rn 'control.Send\|control.Listen\|\.Values()' --include='*.go' .`
Expected: locate the daemon's pick sender (manager) and confirm the bridge no longer listens (removed in M5).

- [ ] **Step 2: Route picks through the session's hub connection**

The daemon already holds the session's `*control.Conn` (in `RunSession`). Expose a per-session pick injection: add to `sessionDriver` a method that enqueues a `pick` frame to the bridge, and a hub-level lookup by session name. Wire the manager's existing pick handler to call it instead of `control.Send`. (Concrete: add `func (d *sessionDriver) Pick(value string)` that does `d.queue <- contracts.Event{T:"pick", Value:value}`, and a registry `map[string]*sessionDriver` keyed by session name populated in `RunHub`.) Write a test asserting `Pick` enqueues a `pick` frame that `pump` forwards.

- [ ] **Step 3: Delete the one-shot pick API**

Once no caller references `control.Server`/`Listen`/`Send`, delete them from `core/internal/control/control.go`, keeping only `SocketPath` (still used) and the `Conn`/`Acceptor`/`Dial` API. Update/trim `control_test.go` accordingly.

- [ ] **Step 4: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add -A
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "refactor(control): route picks over persistent Conn; drop one-shot pick API"
```

### Task 6.2: prune dead `bridge.Options` fields

**Files:**
- Modify: `core/bridge/bridge.go`, `bridge.go` (root)

- [ ] **Step 1: Remove unused Options fields**

Delete `Ensure`, `After`, `Participants`, `ControlSocket` from `bridge.Options` and their flag registrations/pass-through in root `bridge.go`, plus the `--participants` arg in `supervisor.bridgeArgs` (the daemon hub owns participant journaling now, or it is dropped — verify `/session who` still has a source; if the hub must journal participants, add that to the driver's `poll` as a follow-up and keep the flag). Confirm `go vet ./...` reports no unused.

- [ ] **Step 2: Build + suite + commit**

Run: `go build ./... && go test ./...`
```bash
git add -A
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "chore(bridge): prune options dead in pure-runner mode"
```

### Task 6.3: tag contracts v0.1.2 and drop the replace

**Files:**
- Modify: `/home/shan/dev/herrscher/go.mod`

- [ ] **Step 1: Tag and push contracts**

```bash
cd /home/shan/dev/herrscher-contracts
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com tag v0.1.2
git push origin master --tags
```

- [ ] **Step 2: Point herrscher at the tagged version, drop the replace**

```bash
cd /home/shan/dev/herrscher
go mod edit -dropreplace github.com/Herrscherd/herrscher-contracts
go get github.com/Herrscherd/herrscher-contracts@v0.1.2
go mod tidy
```
Expected: `go.mod` requires `herrscher-contracts v0.1.2`, no `replace`.

- [ ] **Step 3: Build + full suite from a clean module cache**

Run: `go build ./... && go test ./...`
Expected: green against the published tag.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -m "chore: depend on herrscher-contracts v0.1.2 (drop dev replace)"
```

---

## Final review (after all milestones)

Dispatch a final code-reviewer over the whole branch (the subagent-driven-development skill's final step), then run `superpowers:finishing-a-development-branch`. Verify:
- `go test ./...` green including `TestCorePurity`/`TestHostPurity`.
- Terminal MVP: serve in a TTY → TUI streams a turn end-to-end.
- Discord: a message round-trips through the hub with unchanged progress/threading/reaction behavior.
- No `control.Send`/one-shot pick path remains; `Event` lives in contracts; the bridge is hub-mode only.

---

## Self-Review (plan vs spec)

**Spec coverage:**
- Pure-runner topology (daemon owns gateways, bridge is a runner) → M2 (runner mode) + M4 (hub) + M5 (Discord relocation) + M5.3 (delete polling path). ✓
- Terminal-first then Discord sequencing → MVP at M4 (terminal only), Discord at M5. ✓
- FIFO turn model, fan-out to all bound gateways → Task 4.1 (`sessionDriver`, `TestDriverFIFOSerializesTurns`, `TestDriverFanOutToAllBoundGateways`). ✓
- Persistent bidirectional socket, daemon listens / bridge dials+redials, mid-turn loss → next input → M1 (`Conn`/`Accept`/`Dial`) + Task 4.2 (`RunSession` reconnect, `TestRunSessionReconnectsAndResumes`). ✓
- Supervisor passes `--hub-socket` → Task 4.3. ✓
- TUI in-process, TTY-gated, background service preserved → Task 4.5 (`term.IsTerminal`, headless `RunHub`). ✓
- Purity preserved (terminal = plugin) → terminal under `plugins/`, blank-imported; purity checks asserted each milestone. ✓
- `EventSink` capability + `Event` in contracts (user's chosen approach) → M0. ✓
- Out-of-scope items (interruption, remote attach, replay, **native menus**) → documented; native menus explicitly deferred in Task 5.2 with the transport left in place. ✓

**Placeholder scan:** No "TBD"/"implement later". Two honestly-flagged scope deferrals (native select menus; participant journaling relocation) are called out with rationale and the residual wiring noted, not left as silent gaps.

**Type consistency:** `contracts.Event{T,Who,Text,Value,Done}` used uniformly; `contracts.EventSink.Emit(Event)` consistent across terminal/renderer/Conn; `control.Conn` methods (`Write`/`Emit`/`Scan`/`Close`), `control.Accept`→`*Acceptor.Conns()`, `control.Dial`→`*Conn` consistent M1↔M2↔M4; `bridge.Options.HubSocket`, `bridge.Run(ctx, newBackend, orch, o)` (M5 signature) consistent with root call site; `host.RunHub`/`RunSession`/`newSessionDriver`/`newGatewayRenderer` consistent.
