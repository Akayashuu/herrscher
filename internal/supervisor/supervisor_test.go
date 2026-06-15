package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/Akayashuu/herrscher/internal/control"
	"github.com/Akayashuu/herrscher/internal/state"
)

func TestBridgeArgsIncludeControlSocketForTmux(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	want := control.SocketPath("demo")
	// Only the explicit tmux backend gets a control socket for choice routing.
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: "tmux"})
	if !strings.Contains(strings.Join(args, " "), "--control-socket "+want) {
		t.Fatalf("tmux backend: expected --control-socket %s in %v", want, args)
	}
	// The default (empty → stream) and explicit stream backends have no pane to
	// type into → no control socket.
	for _, backend := range []string{"", "stream"} {
		args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: backend})
		if strings.Contains(strings.Join(args, " "), "--control-socket") {
			t.Fatalf("backend %q should not get a control socket: %v", backend, args)
		}
	}
}

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
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: "tmux"})
	if !strings.Contains(strings.Join(args, " "), "--backend tmux") {
		t.Fatalf("expected --backend tmux in args: %v", args)
	}
}

func TestBridgeArgsIncludeInitPrompts(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Backend: "tmux", InitPrompts: []string{"read CLAUDE.md", "wait"}})
	joined := strings.Join(args, " ")
	// One --tmux-init per prompt, in order, so a respawn replays the same priming.
	if strings.Count(joined, "--tmux-init") != 2 {
		t.Fatalf("expected two --tmux-init flags: %v", args)
	}
	if !strings.Contains(joined, "--tmux-init read CLAUDE.md") || !strings.Contains(joined, "--tmux-init wait") {
		t.Fatalf("expected each prompt passed through: %v", args)
	}
}

func TestBridgeArgsNoInitPromptsWhenEmpty(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/dctl")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	if strings.Contains(strings.Join(args, " "), "--tmux-init") {
		t.Fatalf("no --tmux-init expected without prompts: %v", args)
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
