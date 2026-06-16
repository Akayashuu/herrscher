package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher/core/internal/state"
)

func TestBridgeArgsIncludeParticipants(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	s.PartDir = "/var/dctl"
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--participants") ||
		!strings.Contains(joined, state.ParticipantsPath("/var/dctl", "demo")) {
		t.Fatalf("expected --participants <journal> in args: %v", args)
	}
}

func TestBridgeArgsIncludeAllowlist(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	s.StatePath = "/var/dctl/state.json"
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--allow-state /var/dctl/state.json") {
		t.Fatalf("expected --allow-state <state.json> in args: %v", args)
	}
	if !strings.Contains(joined, "--allow-session demo") {
		t.Fatalf("expected --allow-session <name> in args: %v", args)
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

func TestBridgeArgsNoAllowlistWhenStatePathEmpty(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	if strings.Contains(strings.Join(args, " "), "--allow-state") {
		t.Fatalf("no --allow-state expected when StatePath is empty: %v", args)
	}
}
