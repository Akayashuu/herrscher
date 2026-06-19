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

// Write sends one Event as a JSON line.
func (c *Conn) Write(e contracts.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteEvent(c.c, e)
}

// Emit satisfies contracts.EventSink so the bridge can hand a Conn directly to
// its turn loop as the event sink. A write error is dropped: emission is
// best-effort and the hub's read side detects a dead conn independently.
func (c *Conn) Emit(e contracts.Event) { _ = c.Write(e) }

// Scan reads events until the peer closes the connection or fn returns an error,
// calling fn for each. A clean peer close (io.EOF) yields nil; fn's error (or a
// malformed-line / read error) is returned otherwise.
func (c *Conn) Scan(fn func(contracts.Event) error) error {
	return ScanEvents(c.c, fn)
}

func (c *Conn) Close() error { return c.c.Close() }

// Acceptor listens on a control socket and yields one persistent Conn per
// dialing bridge, keeping each connection open for the session's life.
type Acceptor struct {
	ln    net.Listener
	path  string
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
	a := &Acceptor{ln: ln, path: path, conns: make(chan *Conn, 1)}
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
	_ = os.Remove(a.path)
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
