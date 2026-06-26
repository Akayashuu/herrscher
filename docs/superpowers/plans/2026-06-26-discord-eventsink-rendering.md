# Discord EventSink Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all Discord-specific rendering (progress view, emojis, 2000-char/throttle limits, ACK reactions) out of the generic host and into the Discord gateway by having the gateway implement `contracts.EventSink`; then strip the host's rich fallback renderer.

**Architecture:** The host already forks in `fanOut` (`core/host/turnloop.go`): a gateway implementing `EventSink` gets the raw event stream via `Emit(Event)`; otherwise the host renders. We make `*discord.Gateway` implement `EventSink` and render via its DCTL client, then delete the host's rich `gatewayRenderer`/`progress.go` so the non-EventSink path only posts the final reply.

**Tech Stack:** Go, `github.com/Herrscherd/dctl`, `github.com/Herrscherd/herrscher-contracts`. Two repos: `/home/shan/dev/herrscher-discord-gateway` (gateway) and `/home/shan/dev/herrscher` (host).

**Execution order is safety-critical:** do all gateway tasks (1–6) first and verify; only then do host tasks (7–9). The host keeps working via the fallback the whole time the gateway is being changed.

---

## Background facts (verified)

- `contracts.EventSink` is `Emit(Event)`. `contracts.Event{T, Who, Text, Value, Done, Cost}` — **no channel, no message ID**.
- Event types on the bus: `human` (turn start), `status` (tool line, text is `"Tool detail"`), `chunk` (assistant prose), `reset` (discard partial turn), `reply` (with `Done` + `Cost`). Also `input`/`pick` (not rendered).
- The host renderer maps them in `core/host/renderer.go` `handle()`; reference implementation to port lives in `core/host/progress.go` (`progressView`) + `core/host/renderer.go` (`splitTool`, `chunk`, event mapping).
- Gateway model is **mono-channel** (one bot + `DefaultChannel`). Render to `DefaultChannel`.
- `*discord.Gateway` (gateway.go) holds a narrow `client` (Send/Reply/React/SendSelectMenu). `*discord.Platform` (adapters.go) holds `*dctl.Client` and implements `UpsertStatusMessage`, `Unreact`, `DefaultChannel`, `Read`. The host checks `EventSink` on the **Gateway** field of the GatewaySet.
- Both `Gateway` and `Platform` are built in `NewGatewaySet` (register.go) from the same `*dctl.Client`.

## File structure

Gateway repo (`/home/shan/dev/herrscher-discord-gateway`):
- Create `progress.go` — `progressView` + render helpers (`emojiFor`, `clip`, `flatten`, `plural`, `formatCost`).
- Create `progress_test.go` — progress view unit tests.
- Create `sink.go` — `sink` type: holds render deps + active progress view + last-user-message id; `handle(Event)` maps the stream; `noteUser(id)`.
- Create `sink_test.go` — sink behaviour tests against a fake render client.
- Modify `gateway.go` — add `Emit` on `*Gateway`, a `sink` field, EventSink compile-time assertion.
- Modify `adapters.go` — `Platform` records last user message id in `Read` via the shared sink.
- Modify `register.go` — construct one `sink`, inject into both `Gateway` and `Platform`.

Host repo (`/home/shan/dev/herrscher`):
- Delete `core/host/progress.go`.
- Modify `core/host/renderer.go` — reduce to a minimal "post final reply only" renderer (no emojis, no progress view, no Discord constants); keep `chunk`.
- Modify `core/host/turnloop.go` — `fanOut` non-EventSink branch posts only the final reply.
- Modify `core/host/renderer_test.go`, `core/host/turnloop_test.go` — adjust expectations.
- Modify `go.mod` — bump `herrscher-discord-gateway` to the new version.

---

## Task 1: Port the progress view into the gateway

**Files:**
- Create: `/home/shan/dev/herrscher-discord-gateway/progress.go`
- Test: `/home/shan/dev/herrscher-discord-gateway/progress_test.go`

- [ ] **Step 1: Write the failing test**

Create `progress_test.go`:

```go
package discord

import (
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestProgressViewRendersToolLineWithEmoji(t *testing.T) {
	var last string
	pv := newProgressView(func(id, content string) (string, error) {
		last = content
		return "m1", nil
	}, "full", time.Unix(0, 0))

	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read", Detail: "envfile.go"})
	pv.flush(true)

	if !strings.Contains(last, "📖 Read") || !strings.Contains(last, "envfile.go") {
		t.Fatalf("progress body = %q, want emoji+tool+detail", last)
	}
}

func TestProgressViewSummaryCountsActions(t *testing.T) {
	posted := []string{}
	pv := newProgressView(func(id, content string) (string, error) {
		posted = append(posted, content)
		return "m1", nil
	}, "full", time.Unix(0, 0))
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read"})
	pv.add(contracts.BackendEvent{Kind: "tool", Tool: "Read"})
	pv.add(contracts.BackendEvent{Kind: "result", Cost: 0.02})
	pv.finish(false)

	got := posted[len(posted)-1]
	if !strings.HasPrefix(got, "✅") || !strings.Contains(got, "2 actions") || !strings.Contains(got, "Read×2") {
		t.Fatalf("summary = %q, want ✅ 2 actions (Read×2)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestProgressView`
Expected: FAIL — `undefined: newProgressView`.

- [ ] **Step 3: Write the implementation**

Create `progress.go` (ported from `herrscher/core/host/progress.go`, with the host-only `keep` mode and live-message bookkeeping retained; `nowFunc` replaced by a `now func() time.Time` field so tests pin the clock without a package global):

```go
package discord

import (
	"fmt"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// maxLines caps the live progress body so it stays under Discord's 2000-char
// message limit; older lines are elided with a leading "…".
const maxLines = 15

// progressInterval throttles live edits so a tool-heavy turn does not hammer
// Discord's per-channel edit rate limit. Events are coalesced between edits.
const progressInterval = 1500 * time.Millisecond

// progressView accumulates one turn's activity and pushes it to a single
// live-updating Discord message, then collapses it to a one-line summary. post
// creates (empty id) or edits (non-empty id) the message and returns its id.
type progressView struct {
	post     func(msgID, content string) (string, error)
	level    string // "actions" | "full"
	start    time.Time
	now      func() time.Time
	lines    []string
	counts   map[string]int
	order    []string
	cost     float64
	actions  int
	msgID    string
	lastEdit time.Time
	dirty    bool
}

func newProgressView(post func(string, string) (string, error), level string, start time.Time) *progressView {
	return &progressView{post: post, level: level, start: start, now: time.Now, counts: map[string]int{}}
}

func (p *progressView) add(ev contracts.BackendEvent) {
	switch ev.Kind {
	case "result":
		p.cost = ev.Cost
		return
	case "reset":
		p.lines = nil
		p.order = nil
		p.cost = 0
		p.actions = 0
		p.counts = map[string]int{}
		return
	case "text":
		if p.level != "full" {
			return
		}
		p.lines = append(p.lines, "💭 "+clip(flatten(ev.Detail), 120))
	case "tool":
		if _, seen := p.counts[ev.Tool]; !seen {
			p.order = append(p.order, ev.Tool)
		}
		p.counts[ev.Tool]++
		p.actions++
		line := emojiFor(ev.Tool) + " " + ev.Tool
		if d := clip(flatten(ev.Detail), 120); d != "" {
			line += " · " + d
		}
		p.lines = append(p.lines, line)
	default:
		return
	}
	p.dirty = true
	p.flush(false)
}

func (p *progressView) flush(force bool) {
	if !p.dirty || p.post == nil {
		return
	}
	if !force && !p.lastEdit.IsZero() && p.now().Sub(p.lastEdit) < progressInterval {
		return
	}
	id, err := p.post(p.msgID, p.render())
	if err != nil {
		return
	}
	p.msgID = id
	p.lastEdit = p.now()
	p.dirty = false
}

func (p *progressView) finish(failed bool) {
	if len(p.lines) == 0 {
		if p.msgID != "" && p.post != nil {
			_, _ = p.post(p.msgID, p.summary(failed))
		}
		return
	}
	if p.post != nil {
		_, _ = p.post(p.msgID, p.summary(failed))
	}
}

func (p *progressView) render() string {
	lines := p.lines
	var b strings.Builder
	b.WriteString("⏳ en cours…\n")
	if len(lines) > maxLines {
		b.WriteString("…\n")
		lines = lines[len(lines)-maxLines:]
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

func (p *progressView) summary(failed bool) string {
	icon := "✅"
	if failed {
		icon = "⚠️"
	}
	parts := make([]string, 0, len(p.order))
	for _, name := range p.order {
		if n := p.counts[name]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, n))
		} else {
			parts = append(parts, name)
		}
	}
	var s string
	if p.actions == 0 {
		s = icon + " terminé"
	} else {
		s = fmt.Sprintf("%s %d action%s", icon, p.actions, plural(p.actions))
		if len(parts) > 0 {
			s += " (" + strings.Join(parts, ", ") + ")"
		}
	}
	s += fmt.Sprintf(" · %ds", int(p.now().Sub(p.start).Round(time.Second).Seconds()))
	if p.cost > 0 {
		s += " · " + formatCost(p.cost)
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func formatCost(c float64) string {
	if c < 0.01 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

func emojiFor(tool string) string {
	switch tool {
	case "Read":
		return "📖"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "✏️"
	case "Grep", "Glob":
		return "🔎"
	case "Task", "Agent":
		return "🤖"
	case "WebFetch", "WebSearch":
		return "🌐"
	case "TodoWrite":
		return "📝"
	default:
		return "🔧"
	}
}

func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestProgressView`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-discord-gateway
git add progress.go progress_test.go
git commit -m "feat: port progress view + emojis into the gateway"
```

---

## Task 2: Add the `sink` (EventSink renderer) with a render-client seam

**Files:**
- Create: `/home/shan/dev/herrscher-discord-gateway/sink.go`
- Test: `/home/shan/dev/herrscher-discord-gateway/sink_test.go`

The `sink` maps the raw event stream the way `herrscher/core/host/renderer.go handle()` does, plus the ⏳ ACK. It depends on a narrow `renderClient` seam (faked in tests). `chunkText`/`splitTool` are ported here.

- [ ] **Step 1: Write the failing test**

Create `sink_test.go`:

```go
package discord

import (
	"context"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type fakeRender struct {
	channel   string
	upserts   []string // status contents in order
	posts     []string // final replies posted
	reacted   []string // emojis added
	unreacted []string // emojis removed
	statusID  string
}

func (f *fakeRender) DefaultChannel() string { return f.channel }
func (f *fakeRender) UpsertStatusMessage(_ context.Context, _, id, content string) (string, error) {
	f.upserts = append(f.upserts, content)
	if f.statusID == "" {
		f.statusID = "status1"
	}
	return f.statusID, nil
}
func (f *fakeRender) Post(_ context.Context, _, content string) error {
	f.posts = append(f.posts, content)
	return nil
}
func (f *fakeRender) React(_ context.Context, _, _, emoji string) error {
	f.reacted = append(f.reacted, emoji)
	return nil
}
func (f *fakeRender) Unreact(_ context.Context, _, _, emoji string) error {
	f.unreacted = append(f.unreacted, emoji)
	return nil
}

func newTestSink(f *fakeRender) *sink {
	s := newSink(context.Background(), f, "full")
	return s
}

func TestSinkAcksHumanAndSummarizesReply(t *testing.T) {
	f := &fakeRender{channel: "c1"}
	s := newTestSink(f)
	s.noteUser("u1")

	s.handle(contracts.Event{T: "human", Who: "alice", Text: "hi"})
	if len(f.reacted) != 1 || f.reacted[0] != ackEmoji {
		t.Fatalf("reacted = %v, want one %q", f.reacted, ackEmoji)
	}
	s.handle(contracts.Event{T: "status", Text: "Read envfile.go"})
	s.handle(contracts.Event{T: "reply", Text: "done", Done: true, Cost: 0.02})

	if len(f.posts) != 1 || f.posts[0] != "done" {
		t.Fatalf("posts = %v, want [done]", f.posts)
	}
	if len(f.unreacted) != 1 || f.unreacted[0] != ackEmoji {
		t.Fatalf("unreacted = %v, want one %q", f.unreacted, ackEmoji)
	}
	last := f.upserts[len(f.upserts)-1]
	if !strings.HasPrefix(last, "✅") {
		t.Fatalf("final status = %q, want ✅ summary", last)
	}
}

func TestSinkChunksLongReply(t *testing.T) {
	f := &fakeRender{channel: "c1"}
	s := newTestSink(f)
	s.handle(contracts.Event{T: "human"})
	long := strings.Repeat("x", gatewayMaxLen+50)
	s.handle(contracts.Event{T: "reply", Text: long, Done: true})
	if len(f.posts) != 2 {
		t.Fatalf("posts = %d chunks, want 2", len(f.posts))
	}
}

func TestSinkResetCollapsesProgress(t *testing.T) {
	f := &fakeRender{channel: "c1"}
	s := newTestSink(f)
	s.handle(contracts.Event{T: "human"})
	s.handle(contracts.Event{T: "status", Text: "Read x"})
	s.handle(contracts.Event{T: "reset"})
	last := f.upserts[len(f.upserts)-1]
	if !strings.HasPrefix(last, "⚠️") {
		t.Fatalf("reset status = %q, want ⚠️ summary", last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestSink`
Expected: FAIL — `undefined: sink` / `newSink` / `ackEmoji` / `gatewayMaxLen`.

- [ ] **Step 3: Write the implementation**

Create `sink.go`:

```go
package discord

import (
	"context"
	"strings"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// gatewayMaxLen is Discord's hard per-message limit; long replies are chunked.
const gatewayMaxLen = 2000

// ackEmoji marks a received turn on the triggering user message; removed when
// the turn finishes.
const ackEmoji = "⏳"

// renderClient is the narrow Discord surface the sink needs (faked in tests).
type renderClient interface {
	DefaultChannel() string
	UpsertStatusMessage(ctx context.Context, channelID, messageID, content string) (string, error)
	Post(ctx context.Context, channelID, content string) error
	React(ctx context.Context, channelID, messageID, emoji string) error
	Unreact(ctx context.Context, channelID, messageID, emoji string) error
}

// sink renders the live turn-event stream onto Discord. Mono-channel: one
// in-flight turn at a time, guarded by mu. It is shared between the Gateway
// (Emit) and the Platform (Read records the last user message id for the ACK).
type sink struct {
	ctx   context.Context
	rc    renderClient
	level string

	mu       sync.Mutex
	pv       *progressView
	lastUser string // id of the message that triggered the current/next turn
	acked    string // id currently carrying the ⏳ reaction ("" if none)
}

func newSink(ctx context.Context, rc renderClient, level string) *sink {
	if level == "" {
		level = "full"
	}
	return &sink{ctx: ctx, rc: rc, level: level}
}

// noteUser records the id of the latest user (non-bot) message, so the next
// turn's ACK reaction lands on it.
func (s *sink) noteUser(id string) {
	s.mu.Lock()
	s.lastUser = id
	s.mu.Unlock()
}

// Emit satisfies contracts.EventSink.
func (s *sink) handle(e contracts.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.rc.DefaultChannel()

	switch e.T {
	case "human":
		post := func(id, content string) (string, error) {
			return s.rc.UpsertStatusMessage(s.ctx, ch, id, content)
		}
		s.pv = newProgressView(post, s.level, time.Now())
		if s.lastUser != "" {
			if err := s.rc.React(s.ctx, ch, s.lastUser, ackEmoji); err == nil {
				s.acked = s.lastUser
			}
		}
	case "status":
		if s.pv != nil {
			tool, detail := splitTool(e.Text)
			s.pv.add(contracts.BackendEvent{Kind: "tool", Tool: tool, Detail: detail})
		}
	case "chunk":
		if s.pv != nil {
			s.pv.add(contracts.BackendEvent{Kind: "text", Detail: e.Text})
		}
	case "reset":
		if s.pv != nil {
			s.pv.finish(true)
			s.pv = nil
		}
		s.clearAck(ch)
	case "reply":
		if !e.Done {
			return
		}
		if e.Text != "" {
			for _, part := range chunkText(e.Text, gatewayMaxLen) {
				_ = s.rc.Post(s.ctx, ch, part)
			}
		}
		if s.pv != nil {
			if e.Cost > 0 {
				s.pv.add(contracts.BackendEvent{Kind: "result", Cost: e.Cost})
			}
			s.pv.finish(false)
			s.pv = nil
		}
		s.clearAck(ch)
	}
}

// clearAck removes the ⏳ reaction left on the triggering message, if any.
func (s *sink) clearAck(ch string) {
	if s.acked == "" {
		return
	}
	_ = s.rc.Unreact(s.ctx, ch, s.acked, ackEmoji)
	s.acked = ""
}

// splitTool recovers the tool name and detail from a status line emitted as
// "Tool Detail" so the progress view can group and icon by tool name.
func splitTool(s string) (tool, detail string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// chunkText splits s into pieces no longer than max, preferring newline breaks.
func chunkText(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		if nl := strings.LastIndexByte(s[:max], '\n'); nl > max/2 {
			cut = nl
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestSink`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-discord-gateway
git add sink.go sink_test.go
git commit -m "feat: add EventSink renderer (progress + reply + ACK) behind a render seam"
```

---

## Task 3: Make `*Gateway` implement `contracts.EventSink`

**Files:**
- Modify: `/home/shan/dev/herrscher-discord-gateway/gateway.go`
- Test: `/home/shan/dev/herrscher-discord-gateway/gateway_test.go`

- [ ] **Step 1: Write the failing test**

Append to `gateway_test.go`:

```go
func TestGatewayImplementsEventSink(t *testing.T) {
	var _ contracts.EventSink = (*Gateway)(nil)
}

func TestGatewayEmitForwardsToSink(t *testing.T) {
	f := &fakeRender{channel: "c1"}
	g := NewGateway(&fakeClient{})
	g.sink = newSink(context.Background(), f, "full")
	g.Emit(contracts.Event{T: "human"})
	g.Emit(contracts.Event{T: "reply", Text: "ok", Done: true})
	if len(f.posts) != 1 || f.posts[0] != "ok" {
		t.Fatalf("posts = %v, want [ok]", f.posts)
	}
}
```

(If `gateway_test.go` lacks the `context` import, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestGateway`
Expected: FAIL — `g.sink undefined` / `g.Emit undefined`.

- [ ] **Step 3: Write the implementation**

In `gateway.go`, add the `sink` field and `Emit`, and a compile-time assertion. Change the `Gateway` struct and var block:

```go
var (
	_ contracts.Gateway                = (*Gateway)(nil)
	_ contracts.SessionControlReceiver = (*Gateway)(nil)
	_ contracts.EventSink              = (*Gateway)(nil)
)

// Gateway adapts the Discord REST client to contracts.Gateway. When built from
// real config it also carries a slash runtime and a rendering sink, so it drives
// the slash surface (SessionControlReceiver) and renders the live turn stream
// (EventSink).
type Gateway struct {
	c     client
	slash *slash
	sink  *sink
}
```

Add the method (near `Post`):

```go
// Emit renders one live turn event onto Discord. It satisfies
// contracts.EventSink; a Gateway built without a sink (e.g. in some tests)
// drops events rather than panicking.
func (g *Gateway) Emit(e contracts.Event) {
	if g.sink == nil {
		return
	}
	g.sink.handle(e)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestGateway`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-discord-gateway
git add gateway.go gateway_test.go
git commit -m "feat: Gateway implements EventSink, delegating to the render sink"
```

---

## Task 4: `Platform.Read` records the last user message id

**Files:**
- Modify: `/home/shan/dev/herrscher-discord-gateway/adapters.go`
- Test: `/home/shan/dev/herrscher-discord-gateway/adapters_test.go` (create if absent)

The host's poll loop reads via `Platform.Read` and drops bot messages. We mirror that here: after building the result, record the newest non-bot message id on the shared sink so the next `human` ACK lands on it.

- [ ] **Step 1: Write the failing test**

Create or append `adapters_test.go`:

```go
package discord

import (
	"context"
	"testing"
)

func TestPlatformReadNotesLastUserToSink(t *testing.T) {
	f := &fakeRender{channel: "c1"}
	s := newSink(context.Background(), f, "full")
	p := &Platform{sink: s} // c left nil: readImpl is injected below

	p.readImpl = func(context.Context, string, int, string) ([]rawMsg, error) {
		return []rawMsg{
			{id: "1", bot: false},
			{id: "2", bot: true},
			{id: "3", bot: false},
		}, nil
	}

	if _, err := p.Read(context.Background(), "c1", 100, ""); err != nil {
		t.Fatal(err)
	}
	if s.lastUser != "3" {
		t.Fatalf("lastUser = %q, want 3", s.lastUser)
	}
}
```

This test requires `Platform` to expose a small injectable read seam (`readImpl` returning `[]rawMsg`) so it is unit-testable without a live dctl client. Introduce `rawMsg{id string; bot bool}` and have the real `Read` adapt dctl messages into it.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestPlatformRead`
Expected: FAIL — `p.sink undefined` / `p.readImpl undefined` / `rawMsg undefined`.

- [ ] **Step 3: Write the implementation**

In `adapters.go`, extend `Platform` and refactor `Read` to go through an injectable seam:

```go
type Platform struct {
	c        *dctl.Client
	sink     *sink
	readImpl func(ctx context.Context, channelID string, limit int, after string) ([]rawMsg, error)
}

// rawMsg is the minimal shape Read needs to track the last user message.
type rawMsg struct {
	id  string
	bot bool
	msg contracts.Message
}

func NewPlatform(c *dctl.Client) *Platform {
	p := &Platform{c: c}
	p.readImpl = p.readDctl
	return p
}

// readDctl is the production read seam: it pulls messages via dctl and adapts
// them to rawMsg (carrying the fully-mapped contracts.Message).
func (p *Platform) readDctl(ctx context.Context, channelID string, limit int, after string) ([]rawMsg, error) {
	msgs, err := p.c.Messages().Read(ctx, channelID, limit, after)
	if err != nil {
		return nil, err
	}
	out := make([]rawMsg, 0, len(msgs))
	for _, m := range msgs {
		atts := make([]contracts.Attachment, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			atts = append(atts, contracts.Attachment{
				Filename:    a.Filename,
				URL:         a.URL,
				ContentType: a.ContentType,
				Size:        a.Size,
			})
		}
		out = append(out, rawMsg{
			id:  m.ID,
			bot: m.Author.Bot,
			msg: contracts.Message{
				ID:          m.ID,
				ChannelID:   m.ChannelID,
				Content:     m.Content,
				AuthorID:    m.Author.ID,
				AuthorName:  m.Author.Username,
				AuthorBot:   m.Author.Bot,
				Attachments: atts,
			},
		})
	}
	return out, nil
}

func (p *Platform) Read(ctx context.Context, channelID string, limit int, after string) ([]contracts.Message, error) {
	raws, err := p.readImpl(ctx, channelID, limit, after)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.Message, 0, len(raws))
	for _, r := range raws {
		out = append(out, r.msg)
		if !r.bot && p.sink != nil {
			p.sink.noteUser(r.id) // newest non-bot id wins (messages are oldest→newest)
		}
	}
	return out, nil
}
```

Note: confirm the dctl `Read` ordering. If `Messages().Read` returns newest→oldest, record only the **first** non-bot id instead. Verify against the host poll semantics in `herrscher/core/host/turnloop.go:134-141` (it sets `last = m.ID` per message in iteration order and uses it as the `after` cursor, implying oldest→newest). Keep the loop matching that ordering.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestPlatformRead`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-discord-gateway
git add adapters.go adapters_test.go
git commit -m "feat: Platform.Read records last user message id for the ACK"
```

---

## Task 5: Wire the shared sink in `NewGatewaySet`

**Files:**
- Modify: `/home/shan/dev/herrscher-discord-gateway/register.go`
- Modify: `/home/shan/dev/herrscher-discord-gateway/adapters.go` (add a render adapter so `*Platform`+client satisfy `renderClient`)

The sink needs a `renderClient` (DefaultChannel/UpsertStatusMessage/Post/React/Unreact). `Platform` already has DefaultChannel/UpsertStatusMessage/Unreact; add `Post` and `React` to a thin adapter that also reaches the dctl client.

- [ ] **Step 1: Write the failing test**

Append to `adapters_test.go`:

```go
func TestPlatformSatisfiesRenderClientViaAdapter(t *testing.T) {
	var _ renderClient = (*renderAdapter)(nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go test ./... -run TestPlatformSatisfies`
Expected: FAIL — `undefined: renderAdapter`.

- [ ] **Step 3: Write the implementation**

In `adapters.go`, add:

```go
// renderAdapter exposes the exact renderClient surface the sink needs, backed by
// the dctl client. DefaultChannel/UpsertStatusMessage/Unreact reuse Platform's
// logic; Post/React go straight to dctl.
type renderAdapter struct{ p *Platform }

func (r renderAdapter) DefaultChannel() string { return r.p.DefaultChannel() }
func (r renderAdapter) UpsertStatusMessage(ctx context.Context, ch, id, content string) (string, error) {
	return r.p.UpsertStatusMessage(ctx, ch, id, content)
}
func (r renderAdapter) Unreact(ctx context.Context, ch, id, emoji string) error {
	return r.p.Unreact(ctx, ch, id, emoji)
}
func (r renderAdapter) Post(ctx context.Context, ch, content string) error {
	_, err := r.p.c.Messages().Send(ctx, ch, content)
	return err
}
func (r renderAdapter) React(ctx context.Context, ch, id, emoji string) error {
	return r.p.c.Reactions().Add(ctx, ch, id, emoji)
}

var _ renderClient = (*renderAdapter)(nil)
```

In `register.go`, build the platform, sink, and inject into both gateway and platform:

```go
func NewGatewaySet(ctx context.Context, cfg contracts.PluginConfig) (contracts.GatewaySet, error) {
	token := cfg.Get("token")
	c := dctl.New(token, cfg.Get("channel"))
	gw := NewGateway(discordClient{c})
	plat := NewPlatform(c)

	s := newSink(ctx, renderAdapter{plat}, "full")
	gw.sink = s
	plat.sink = s

	gw.slash = newSlash(ctx, c.Interactions(), token, newAllowStore(allowStorePath()))
	return contracts.GatewaySet{
		Gateway: gw,
		Reader:  plat,
		Admin:   NewChannelAdmin(c),
		Prober:  NewProber(c),
	}, nil
}
```

- [ ] **Step 4: Run the full gateway suite**

Run: `cd /home/shan/dev/herrscher-discord-gateway && go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-discord-gateway
git add register.go adapters.go adapters_test.go
git commit -m "feat: wire shared render sink into the Discord GatewaySet"
```

---

## Task 6: Tag/publish the gateway and bump it in the host

**Files:**
- Modify: `/home/shan/dev/herrscher/go.mod`

- [ ] **Step 1: Tag the gateway**

Determine the next version (current `go.mod` pins `v0.2.1`):

```bash
cd /home/shan/dev/herrscher-discord-gateway
git tag v0.3.0
git push && git push --tags    # if a remote is configured
```

If the host consumes the gateway via a local `replace` or the module cache, adjust accordingly. Check first:

```bash
cd /home/shan/dev/herrscher && go mod edit -json | grep -A2 discord-gateway
```

- [ ] **Step 2: Bump in the host and build**

```bash
cd /home/shan/dev/herrscher
go get github.com/Herrscherd/herrscher-discord-gateway@v0.3.0
go build ./...
```

Expected: build OK. At this point Discord renders via EventSink; the host fallback is now unused by Discord.

- [ ] **Step 3: Commit**

```bash
cd /home/shan/dev/herrscher
git add go.mod go.sum
git commit -m "build: bump herrscher-discord-gateway to v0.3.0 (EventSink rendering)"
```

---

## Task 7: Strip the host's rich renderer down to "post final reply only"

**Files:**
- Delete: `/home/shan/dev/herrscher/core/host/progress.go`
- Modify: `/home/shan/dev/herrscher/core/host/renderer.go`
- Modify: `/home/shan/dev/herrscher/core/host/turnloop.go`

- [ ] **Step 1: Update the renderer test to the new minimal contract**

Read `core/host/renderer_test.go` first. Replace assertions that expect a progress view / emojis / summary with assertions that a non-EventSink gateway receives **only** the final reply (chunked) via `Gateway.Post`, and nothing on `status`/`chunk`/`human`/`reset`. Write the new test body to match the minimal renderer below before changing the implementation.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher && go test ./core/host/ -run TestRenderer`
Expected: FAIL (old renderer still produces a progress view).

- [ ] **Step 3: Delete `progress.go` and minimize `renderer.go`**

```bash
cd /home/shan/dev/herrscher && git rm core/host/progress.go
```

Replace `core/host/renderer.go` with the minimal renderer (drops `progressView`, `splitTool`, `nowFunc`, the `UpsertStatusMessage` progress path; keeps `chunk` and `gatewayMaxLen`):

```go
package host

import (
	"context"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// gatewayMaxLen is the hard per-message chunk limit for gateways that do not
// render themselves. Rich, gateway-specific rendering (progress, emojis, edit
// throttling) lives in the gateway behind contracts.EventSink.
const gatewayMaxLen = 2000

// gatewayRenderer is the minimal fallback for a gateway that does NOT implement
// EventSink: it posts only the final reply through the Gateway port, chunked.
type gatewayRenderer struct {
	gw   contracts.Gateway
	conv contracts.Conversation
}

func newGatewayRenderer(gw contracts.Gateway, ch string) *gatewayRenderer {
	return &gatewayRenderer{
		gw:   gw,
		conv: contracts.Conversation{Gateway: gw.Manifest().Kind, ID: ch},
	}
}

// handle posts the final reply; all other event kinds are ignored.
func (r *gatewayRenderer) handle(ctx context.Context, e contracts.Event) {
	if e.T != "reply" || !e.Done || e.Text == "" {
		return
	}
	for _, part := range chunk(e.Text, gatewayMaxLen) {
		_, _ = r.gw.Post(ctx, r.conv, part)
	}
}

// chunk splits s into pieces no longer than max, preferring to break on a
// newline boundary so multi-line output stays readable.
func chunk(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		if nl := strings.LastIndexByte(s[:max], '\n'); nl > max/2 {
			cut = nl
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
```

Update the `newGatewayRenderer` call in `core/host/turnloop.go` `fanOut` (was `newGatewayRenderer(g.Gateway, g.Reader, gatewayChannel(g), "full")`):

```go
		r := d.renderers[key]
		if r == nil {
			r = newGatewayRenderer(g.Gateway, gatewayChannel(g))
			d.renderers[key] = r
		}
		r.handle(ctx, e)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/shan/dev/herrscher && go build ./... && go test ./core/host/ -run TestRenderer`
Expected: build OK, PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher
git add -A core/host/
git commit -m "refactor(host): drop rich renderer; non-EventSink path posts final reply only"
```

---

## Task 8: Fix remaining host tests referencing the old renderer

**Files:**
- Modify: `/home/shan/dev/herrscher/core/host/turnloop_test.go`
- Modify: any other `core/host/*_test.go` referencing `progressView`, `nowFunc`, `splitTool`, the `"full"` renderer arg, or `UpsertStatusMessage` progress behaviour.

- [ ] **Step 1: Find references**

Run: `cd /home/shan/dev/herrscher && grep -rn "progressView\|nowFunc\|splitTool\|newGatewayRenderer\|UpsertStatusMessage\|emojiFor" core/host/`
Expected: a list of test sites to update.

- [ ] **Step 2: Update each test**

For non-EventSink expectations, assert only that the final reply is posted (chunked) and that `status`/`chunk`/`human`/`reset` produce no gateway calls. For EventSink gateways (the `sinkRecorder` path in `turnloop_test.go`), behaviour is unchanged — the host still fans every event via `Emit`. Remove tests that asserted host-side progress rendering for Discord (now the gateway's responsibility); keep the fan-out ordering tests.

- [ ] **Step 3: Run the full host suite**

Run: `cd /home/shan/dev/herrscher && go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd /home/shan/dev/herrscher
git add core/host/
git commit -m "test(host): update fallback renderer expectations to final-reply-only"
```

---

## Task 9: End-to-end verification and docs

**Files:**
- Modify: `/home/shan/dev/herrscher/IMPLEMENTATION.md` (if it documents the rendering path)

- [ ] **Step 1: Build both repos**

Run:
```bash
cd /home/shan/dev/herrscher-discord-gateway && go vet ./... && go test ./...
cd /home/shan/dev/herrscher && go vet ./... && go test ./...
```
Expected: all PASS.

- [ ] **Step 2: Manual smoke test (if a Discord instance is available)**

Drive a turn end to end and confirm on Discord: a ⏳ reaction appears on your message at turn start; a live progress message updates with tool emojis; the final reply posts (chunked if long); the progress message collapses to a `✅ N actions (… ) · Ns · $…` summary; the ⏳ reaction is removed.

- [ ] **Step 3: Update host docs**

If `IMPLEMENTATION.md` (or any doc) describes the host as owning Discord rendering, update it to state: gateways render their own live stream via `EventSink`; the host fallback posts only the final reply. Commit.

```bash
cd /home/shan/dev/herrscher
git add IMPLEMENTATION.md
git commit -m "docs: host is render-agnostic; gateways own rendering via EventSink"
```

---

## Self-review notes

- **Spec coverage:** EventSink on gateway (T3), progress view + emojis moved (T1), live edits/throttle/2000 in gateway (T1/T2), ACK ⏳ add+remove (T2/T4), host stripped to final-reply-only (T7), host tests fixed (T8), order gateway→host (T1–6 then 7–9). All spec sections mapped.
- **Type consistency:** `sink`/`newSink`/`renderClient`/`renderAdapter`/`progressView`/`newProgressView`/`chunkText` (gateway) vs `gatewayRenderer`/`chunk` (host) — names are distinct per repo and used consistently. `ackEmoji`, `gatewayMaxLen`, `maxLines`, `progressInterval` defined once each in the gateway.
- **Open verification (call out, don't assume):** dctl `Messages().Read` ordering (Task 4 Step 3) — confirm oldest→newest before trusting "newest non-bot id wins". Whether the host pulls the gateway via `replace` vs a tag (Task 6) — check before tagging.
