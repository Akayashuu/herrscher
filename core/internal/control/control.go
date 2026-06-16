// Package control is the bridge's control channel: a per-session Unix domain
// socket the bridge listens on and the daemon writes to. It exists because the
// bridge is REST-poll only and has no Discord gateway, so it cannot receive a
// select-menu click itself. When a user clicks a choice menu, the daemon (which
// does hold the gateway) forwards the picked value over this socket; the bridge
// then injects it into the active backend, serialized with its own turns.
package control

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// SocketPath derives the per-session control socket path. Both the supervisor
// (which passes it to the bridge) and the daemon (which dials it on a click)
// compute the same path from the session name, so no extra coordination state is
// needed. Characters unsafe in a filename are folded to "-".
func SocketPath(session string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, session)
	return filepath.Join(os.TempDir(), "dctl-control-"+safe+".sock")
}

// Server listens on a control socket and surfaces each received value on Values.
// One connection carries one newline-terminated value (the picked digit).
type Server struct {
	ln     net.Listener
	values chan string
}

// Listen binds the control socket at path, removing any stale socket left by a
// previous run first. Each value written by a client appears on Values().
func Listen(path string) (*Server, error) {
	// A leftover socket file from a crashed run would make bind fail with
	// EADDRINUSE; it's safe to remove because the path is ours per session.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, values: make(chan string, 8)}
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			close(s.values)
			return // listener closed → shut the loop down
		}
		// Read a single line then close; the daemon writes one value per dial.
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if v := strings.TrimSpace(line); v != "" {
			s.values <- v
		}
	}
}

// Values yields each value a client sends; it closes when the server closes.
func (s *Server) Values() <-chan string { return s.values }

// Close stops listening and removes the socket file.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.ln.Addr().String())
	return err
}

// Send dials the control socket at path and writes value (one line). It is the
// daemon side: a short-lived connection per click.
func Send(path, value string) error {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(strings.TrimSpace(value) + "\n"))
	return err
}
