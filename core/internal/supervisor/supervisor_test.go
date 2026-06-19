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

func TestBridgeArgsIncludeSession(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	if !strings.Contains(strings.Join(args, " "), "--session demo") {
		t.Fatalf("expected --session <name> in args: %v", args)
	}
}

func TestBridgeArgsIncludeBackend(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: "oneshot"})
	if !strings.Contains(strings.Join(args, " "), "--backend oneshot") {
		t.Fatalf("expected --backend oneshot in args: %v", args)
	}
}

func TestBridgeArgsNoBackendWhenStream(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	for _, b := range []string{"", "stream"} {
		args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: b})
		if strings.Contains(strings.Join(args, " "), "--backend") {
			t.Fatalf("no --backend expected for backend %q: %v", b, args)
		}
	}
}
