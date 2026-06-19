// Package control is the bridge's control channel: a per-session Unix domain
// socket the daemon hub accepts on and the bridge dials (see Conn/Acceptor/Dial
// in conn.go). SocketPath derives the per-session path both sides compute from
// the session name, so no extra coordination state is needed.
package control

import (
	"os"
	"path/filepath"
	"strings"
)

// SocketPath derives the per-session control socket path. Both the supervisor
// (which passes it to the bridge) and the daemon (which accepts on it) compute
// the same path from the session name. Characters unsafe in a filename are
// folded to "-".
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
