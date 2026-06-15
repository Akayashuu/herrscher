package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Akayashuu/herrscher/internal/control"
	"github.com/Akayashuu/herrscher/internal/state"
)

// Supervisor manages one child `dctl bridge` process per session.
type Supervisor struct {
	ctx       context.Context
	selfBin   string // path to the dctl binary (os.Executable)
	PartDir   string // participants journal dir; empty disables --participants
	StatePath string // daemon state.json; empty disables allowlist enforcement
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
}

// bridgeArgs builds the child `dctl bridge` argv for sess.
func (s *Supervisor) bridgeArgs(sess state.Session) []string {
	args := []string{"bridge", "-c", sess.ChannelID, "--cmd", sess.Cmd}
	if sess.Backend != "" && sess.Backend != "stream" {
		args = append(args, "--backend", sess.Backend)
	}
	for _, p := range sess.InitPrompts {
		args = append(args, "--tmux-init", p)
	}
	// tmux-backed sessions get a control socket so the daemon can forward
	// select-menu clicks to the bridge. The path is derived from the session
	// name, so the daemon dials the same one without extra coordination.
	if sess.Backend == "tmux" {
		args = append(args, "--control-socket", control.SocketPath(sess.Name))
	}
	if s.PartDir != "" {
		args = append(args, "--participants", state.ParticipantsPath(s.PartDir, sess.Name))
	}
	if s.StatePath != "" {
		args = append(args, "--allow-state", s.StatePath, "--allow-session", sess.Name)
	}
	return args
}

// NewSupervisor builds a Supervisor bound to ctx.
func NewSupervisor(ctx context.Context, selfBin string) *Supervisor {
	return &Supervisor{ctx: ctx, selfBin: selfBin, cancels: map[string]context.CancelFunc{}}
}

// Start launches a supervised bridge for sess (idempotent per name).
func (s *Supervisor) Start(sess state.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.cancels[sess.Name]; running {
		return nil
	}
	cctx, cancel := context.WithCancel(s.ctx)
	s.cancels[sess.Name] = cancel
	go s.runLoop(cctx, sess)
	return nil
}

// Stop terminates the bridge for name.
func (s *Supervisor) Stop(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.cancels[name]; ok {
		cancel()
		delete(s.cancels, name)
	}
	return nil
}

func (s *Supervisor) runLoop(ctx context.Context, sess state.Session) {
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, s.selfBin, s.bridgeArgs(sess)...)
		if sess.Worktree != "" {
			cmd.Dir = sess.Worktree
		}
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.Env = os.Environ()
		_ = cmd.Run() // returns on exit or ctx cancel
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "supervisor: bridge %q exited, restarting in 3s\n", sess.Name)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
