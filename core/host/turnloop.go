package host

import (
	"context"
	"strconv"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	control "github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// pollInterval is how often the driver polls each bound gateway's Read for new
// inbound lines.
var pollInterval = 50 * time.Millisecond

// sessionDriver owns one session's turn lifecycle: it polls every bound
// gateway's Reader for inbound messages, serializes them through a FIFO, writes
// one input frame to the bridge per turn, and fans the bridge's reply events out
// to all bound gateways. toBridge/fromBridge are the two directions of the
// session's control connection (a *control.Conn in production; channels in
// tests).
type sessionDriver struct {
	name      string
	gateways  []contracts.GatewaySet
	toBridge  chan<- contracts.Event
	from      <-chan contracts.Event
	queue     chan contracts.Event
	renderers map[string]*gatewayRenderer

	// hangup signals that the current connection ended so an in-flight turn is
	// abandoned and the FIFO resumes on reconnect. It is buffered (1) and written
	// non-blockingly by serveConn, so a disconnect while the driver is idle can
	// never wedge serveConn (and thus the reconnect accept loop).
	hangup chan struct{}

	// participants is the journal path for /session who (empty = disabled). The
	// daemon owns gateway I/O now, so it records authors here as it polls them.
	participants string
	seenMu       sync.Mutex
	seen         map[string]bool
}

func newSessionDriver(name string, gws []contracts.GatewaySet, toBridge chan<- contracts.Event, fromBridge <-chan contracts.Event) *sessionDriver {
	return &sessionDriver{
		name:      name,
		gateways:  gws,
		toBridge:  toBridge,
		from:      fromBridge,
		queue:     make(chan contracts.Event, 64),
		renderers: map[string]*gatewayRenderer{},
		hangup:    make(chan struct{}, 1),
		seen:      map[string]bool{},
	}
}

// journal records a message author in the participants journal (idempotent,
// best-effort), so /session who has a source now the bridge no longer journals.
func (d *sessionDriver) journal(authorID string) {
	if d.participants == "" || authorID == "" {
		return
	}
	d.seenMu.Lock()
	first := !d.seen[authorID]
	d.seen[authorID] = true
	d.seenMu.Unlock()
	if first {
		_, _ = state.AppendParticipant(d.participants, authorID)
	}
}

// Pick injects a routed select-menu value into this session's turn queue. The
// bridge answers it out-of-band (serialized with turns) and emits a reply.
func (d *sessionDriver) Pick(value string) {
	d.queue <- contracts.Event{T: "pick", Value: value}
}

// sessionRegistry maps live session names to their driver so an out-of-band
// input — a routed select-menu pick — can reach the right session's FIFO. It is
// populated by RunSession for the session's lifetime.
var sessionRegistry = struct {
	mu sync.Mutex
	m  map[string]*sessionDriver
}{m: map[string]*sessionDriver{}}

func registerDriver(name string, d *sessionDriver) {
	sessionRegistry.mu.Lock()
	sessionRegistry.m[name] = d
	sessionRegistry.mu.Unlock()
}

func unregisterDriver(name string) {
	sessionRegistry.mu.Lock()
	delete(sessionRegistry.m, name)
	sessionRegistry.mu.Unlock()
}

// Pick routes a select-menu value to the named session's driver, returning false
// when no live session by that name is driving.
func Pick(session, value string) bool {
	sessionRegistry.mu.Lock()
	d := sessionRegistry.m[session]
	sessionRegistry.mu.Unlock()
	if d == nil {
		return false
	}
	d.Pick(value)
	return true
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
				d.journal(m.AuthorID)
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

// pump dequeues one input at a time and runs it as a turn — this is the FIFO
// serialization: the next frame is not dequeued until the current turn ends.
func (d *sessionDriver) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-d.queue:
			// A pick is answered out-of-band by the bridge; only a real input
			// opens a turn (and a progress view) on the bound gateways.
			if ev.T == "input" {
				d.fanOut(ctx, contracts.Event{T: "human", Who: ev.Who, Text: ev.Text})
			}
			d.runTurn(ctx, ev)
		}
	}
}

// runTurn hands one frame to the bridge and fans its events out until the turn's
// reply{done}. A stale hangup left by a disconnect that happened between turns is
// drained first so it cannot abort this turn; the send then races the hangup so a
// disconnect before the frame is handed off abandons the turn instead of blocking.
func (d *sessionDriver) runTurn(ctx context.Context, ev contracts.Event) {
	select {
	case <-d.hangup:
	default:
	}
	select {
	case d.toBridge <- ev:
	case <-d.hangup:
		return
	case <-ctx.Done():
		return
	}
	d.awaitTurn(ctx)
}

// awaitTurn fans every event for the current turn to all bound gateways and
// returns when it sees reply{done} (or ctx is cancelled, the bridge closed, or a
// hangup signals the in-flight turn was abandoned on a bridge disconnect). A
// backend "reset" is a mid-turn progress event: it is fanned out and the turn
// continues.
func (d *sessionDriver) awaitTurn(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.hangup:
			return // bridge connection ended; abandon this turn
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
	for i, g := range d.gateways {
		if sink, ok := g.Gateway.(contracts.EventSink); ok {
			sink.Emit(e)
			continue
		}
		key := strconv.Itoa(i) + ":" + g.Gateway.Manifest().Kind
		r := d.renderers[key]
		if r == nil {
			r = newGatewayRenderer(g.Gateway, g.Reader, gatewayChannel(g), "full")
			d.renderers[key] = r
		}
		r.handle(ctx, e)
	}
}

// gatewayChannel returns the default channel for a gateway set, or "" when it
// has no reader.
func gatewayChannel(g contracts.GatewaySet) string {
	if g.Reader != nil {
		return g.Reader.DefaultChannel()
	}
	return ""
}

// RunSession drives one session against a control Acceptor: it bridges the
// persistent Conn (input frames out, event frames in) to a sessionDriver, and
// re-binds to a fresh Conn whenever the bridge reconnects (after a crash +
// supervisor restart). It blocks until ctx is cancelled.
func RunSession(ctx context.Context, name string, gws []contracts.GatewaySet, acc *control.Acceptor, participants string) {
	defer acc.Close() // own the acceptor: close the listener + remove the socket on shutdown
	toBridge := make(chan contracts.Event)
	fromBridge := make(chan contracts.Event)
	d := newSessionDriver(name, gws, toBridge, fromBridge)
	d.participants = participants
	registerDriver(name, d)
	defer unregisterDriver(name)
	go d.run(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case conn, ok := <-acc.Conns():
			if !ok {
				return
			}
			serveConn(ctx, conn, toBridge, fromBridge, d.hangup)
		}
	}
}

// serveConn shuttles frames between the driver and one bridge connection until
// the connection closes or ctx is cancelled. The reader goroutine forwards the
// bridge's events into fromBridge; the writer drains toBridge to the conn.
func serveConn(ctx context.Context, conn *control.Conn, toBridge <-chan contracts.Event, fromBridge chan<- contracts.Event, hangup chan<- struct{}) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	// When this connection ends, tell the driver to abandon any in-flight turn so
	// the next queued input flows to the reconnecting bridge. The send is
	// non-blocking onto a buffered channel: the driver may be idle (no turn to
	// abandon), and blocking here would wedge the reconnect accept loop.
	defer func() {
		select {
		case hangup <- struct{}{}:
		default:
		}
	}()

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
			if cctx.Err() != nil {
				return // connection already dead; don't write into a dying conn
			}
			if err := conn.Write(ev); err != nil {
				return // write failed → connection dead, go re-accept
			}
		}
	}
}
