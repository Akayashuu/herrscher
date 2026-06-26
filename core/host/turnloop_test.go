package host

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	control "github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// fanRecorder is a gateway+reader+sink that records what the hub fans to it and
// can feed inbound lines.
type fanRecorder struct {
	mu          sync.Mutex
	inbound     []contracts.Message
	emitted     []contracts.Event
	posted      []string
	upserts     int
	statuses    []string // content of each UpsertStatusMessage (the live progress view)
	sink        bool     // implements EventSink when true
	readChannel string   // last channel id passed to Read
}

func (f *fanRecorder) feed(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = append(f.inbound, contracts.Message{ID: "m", ChannelID: "c", Content: text, AuthorName: "you"})
}

func (f *fanRecorder) feedFrom(text, authorID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = append(f.inbound, contracts.Message{ID: "m", ChannelID: "c", Content: text, AuthorName: "you", AuthorID: authorID})
}
func (f *fanRecorder) Enabled() bool          { return true }
func (f *fanRecorder) DefaultChannel() string { return "c" }
func (f *fanRecorder) EnsureChannel(context.Context, string, string) (contracts.Channel, error) {
	return contracts.Channel{ID: "c"}, nil
}
func (f *fanRecorder) Read(_ context.Context, channelID string, _ int, _ string) ([]contracts.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readChannel = channelID
	out := f.inbound
	f.inbound = nil
	return out, nil
}
func (f *fanRecorder) Unreact(context.Context, string, string, string) error { return nil }
func (f *fanRecorder) UpsertStatusMessage(_ context.Context, _, _, content string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.statuses = append(f.statuses, content)
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

// sinkRecorder wraps fanRecorder and implements EventSink so the hub fans the
// full event stream to it via Emit (instead of only posting the final reply).
type sinkRecorder struct{ fanRecorder }

func (s *sinkRecorder) Emit(e contracts.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitted = append(s.emitted, e)
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
		a.mu.Lock()
		b.mu.Lock()
		defer a.mu.Unlock()
		defer b.mu.Unlock()
		return len(a.posted) == 1 && a.posted[0] == "world" && len(b.posted) == 1 && b.posted[0] == "world"
	}, "reply fanned to both gateways")
}

// TestDriverPollsSessionChannel proves a session with its own channel bound is
// polled on THAT channel, not the gateway's global DefaultChannel ("c"). This
// guards the regression where the daemon read the empty global channel for every
// session, so the bot never saw messages in a session's own channel.
func TestDriverPollsSessionChannel(t *testing.T) {
	a := &fanRecorder{}
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	d.channel = "sess-chan"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.readChannel == "sess-chan"
	}, "driver polls the session's own channel, not the gateway default")
}

// TestDriverNonEventSinkPostsOnlyFinalReply proves that a gateway that does NOT
// implement EventSink receives only the final reply through Post: mid-turn
// status/chunk events render nothing on the host side (rendering is now the
// gateway's job behind EventSink).
func TestDriverNonEventSinkPostsOnlyFinalReply(t *testing.T) {
	a := &fanRecorder{}
	a.feed("hello")
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	if got := <-toBridge; got.T != "input" || got.Text != "hello" {
		t.Fatalf("driver wrote %+v, want input/hello", got)
	}
	fromBridge <- contracts.Event{T: "status", Text: "running"}
	fromBridge <- contracts.Event{T: "reply", Text: "world", Done: true}

	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.posted) == 1 && a.posted[0] == "world"
	}, "final reply posted")

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.upserts != 0 || len(a.statuses) != 0 {
		t.Fatalf("non-EventSink gateway must not render progress; upserts=%d statuses=%v", a.upserts, a.statuses)
	}
}

// TestDriverEmitsAbandonedOnHangup proves that when an in-flight turn is
// abandoned (the bridge connection drops), the host fans an abstract "abandoned"
// signal to EventSink gateways so they can finalize their live acknowledgement.
// The host emits no emoji/reaction itself — presentation is the gateway's job.
func TestDriverEmitsAbandonedOnHangup(t *testing.T) {
	a := &fanRecorder{}    // input source (non-sink)
	rec := &sinkRecorder{} // EventSink gateway: receives the full stream
	a.feed("hello")

	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1",
		[]contracts.GatewaySet{{Gateway: a, Reader: a}, {Gateway: rec, Reader: rec}},
		toBridge, fromBridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// The turn opens (human) and the input is handed to the bridge.
	if got := <-toBridge; got.T != "input" || got.Text != "hello" {
		t.Fatalf("driver wrote %+v, want input/hello", got)
	}
	// The bridge connection drops mid-turn: the driver abandons the turn.
	d.hangup <- struct{}{}

	waitFor(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		n := len(rec.emitted)
		return n >= 2 && rec.emitted[0].T == "human" && rec.emitted[n-1].T == "abandoned"
	}, "abandoned signal fanned to the EventSink gateway")

	// A non-EventSink gateway posts nothing for an abandoned turn (no final reply).
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.posted) != 0 {
		t.Fatalf("non-EventSink gateway must post nothing on abandon; got %v", a.posted)
	}
}

// TestDriverPickForwardsWithoutOpeningTurn proves Pick enqueues a pick frame
// that pump forwards verbatim to the bridge, and that a pick (unlike an input)
// does NOT open a turn/progress view on the bound gateways.
func TestDriverPickForwardsWithoutOpeningTurn(t *testing.T) {
	a := &fanRecorder{}
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	d.Pick("2")
	select {
	case got := <-toBridge:
		if got.T != "pick" || got.Value != "2" {
			t.Fatalf("driver forwarded %+v, want pick/2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Pick was not forwarded to the bridge")
	}
	// The bridge answers the pick out-of-band with a reply.
	fromBridge <- contracts.Event{T: "reply", Text: "picked", Done: true}
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.posted) == 1 && a.posted[0] == "picked" && a.upserts == 0
	}, "pick reply posted without opening a progress view")
}

// TestPickRegistryRoutesToLiveSession proves the package-level Pick routes to a
// registered session and reports false for an unknown one.
func TestPickRegistryRoutesToLiveSession(t *testing.T) {
	a := &fanRecorder{}
	acc, err := control.Accept(tmpSock(t))
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunSession(ctx, "pickreg", "", []contracts.GatewaySet{{Gateway: a, Reader: a}}, acc, "", nil)

	waitFor(t, func() bool { return Pick("pickreg", "1") }, "Pick routes to the live session")
	if Pick("does-not-exist", "1") {
		t.Fatal("Pick must report false for an unknown session")
	}
}

// TestDriverJournalsParticipants proves the daemon driver records message
// authors in the participants journal (the bridge no longer does), so
// /session who keeps a source in pure-runner mode.
func TestDriverJournalsParticipants(t *testing.T) {
	a := &fanRecorder{}
	a.feedFrom("hi", "u1")
	journal := filepath.Join(t.TempDir(), "demo.log")
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	d.participants = journal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	waitFor(t, func() bool {
		ids := state.ReadParticipants(journal)
		return len(ids) == 1 && ids[0] == "u1"
	}, "author journaled for /session who")
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

// TestDriverResetMidTurnDoesNotEndTurn proves a backend "reset" event mid-turn
// is fanned out (not swallowed) and does NOT complete the turn: the next queued
// input must not pump until the real reply{done} arrives.
func TestDriverResetMidTurnDoesNotEndTurn(t *testing.T) {
	a := &fanRecorder{}    // input source (non-sink: only final reply posted)
	rec := &sinkRecorder{} // EventSink gateway: receives the full stream
	a.feed("first")
	a.feed("second")

	toBridge := make(chan contracts.Event, 8)
	fromBridge := make(chan contracts.Event, 8)
	d := newSessionDriver("s1",
		[]contracts.GatewaySet{{Gateway: a, Reader: a}, {Gateway: rec, Reader: rec}},
		toBridge, fromBridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)

	// First input pumped.
	if got := <-toBridge; got.Text != "first" {
		t.Fatalf("first frame = %+v", got)
	}

	// Mid-turn backend reset followed by a chunk, then the real reply{done}.
	fromBridge <- contracts.Event{T: "reset"}
	fromBridge <- contracts.Event{T: "chunk", Text: "x"}

	// The reset+chunk must be fanned to the EventSink gateway before the reply,
	// and the second input must NOT have pumped yet (turn still alive).
	waitFor(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.emitted) == 3 &&
			rec.emitted[0].T == "human" &&
			rec.emitted[1].T == "reset" && rec.emitted[2].T == "chunk"
	}, "human, reset and chunk fanned to EventSink before reply")

	select {
	case got := <-toBridge:
		t.Fatalf("second frame %+v pumped before reply{done}: reset ended the turn", got)
	case <-time.After(150 * time.Millisecond):
	}

	// Now complete the turn.
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
	go RunSession(ctx, "s1", "", []contracts.GatewaySet{{Gateway: a, Reader: a}}, acc, "", nil)

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

// TestRunSessionReconnectsAfterCompletedTurn covers the case the reconnect test
// above does not: the bridge dies while the driver is IDLE (the previous turn
// already finished), then a new input arrives. The driver must accept the
// reconnecting bridge and deliver the new input.
func TestRunSessionReconnectsAfterCompletedTurn(t *testing.T) {
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
	go RunSession(ctx, "s1", "", []contracts.GatewaySet{{Gateway: a, Reader: a}}, acc, "", nil)

	// First bridge: gets q1, completes the turn with reply{done}, then dies while
	// the driver is idle.
	c1 := dialCtl(t, sock)
	gotInput(t, c1, "q1")
	if err := c1.Write(contracts.Event{T: "reply", Text: "r1", Done: true}); err != nil {
		t.Fatalf("write reply: %v", err)
	}
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.posted) == 1
	}, "first turn completed (reply posted)")
	c1.Close()

	// New bridge connects (supervisor restart); a new input must flow.
	a.feed("q2")
	c2 := dialCtl(t, sock)
	defer c2.Close()
	gotInput(t, c2, "q2")
}

func tmpSock(t *testing.T) string                      { t.Helper(); return filepath.Join(t.TempDir(), "h.sock") }
func acceptCtl(sock string) (*control.Acceptor, error) { return control.Accept(sock) }
func dialCtl(t *testing.T, sock string) *control.Conn {
	t.Helper()
	c, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

// gotInput scans c for the first "input" frame and asserts its Text within 1s.
func gotInput(t *testing.T, c *control.Conn, want string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		_ = c.Scan(func(e contracts.Event) error {
			if e.T == "input" {
				got <- e.Text
				return errStopScan
			}
			return nil
		})
	}()
	select {
	case g := <-got:
		if g != want {
			t.Fatalf("input = %q, want %q", g, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for input %q", want)
	}
}

var errStopScan = stopScanErr{}

type stopScanErr struct{}

func (stopScanErr) Error() string { return "stop" }
