package supervisor

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Herrscherd/herrscher/core/internal/obs"
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

func TestBridgeArgsThreadsMemoryScope(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/herrscher")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1", Project: "obby", Agent: "roblox"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--project obby") {
		t.Fatalf("expected --project for the shared memory scope: %v", args)
	}
	if !strings.Contains(joined, "--agent roblox") {
		t.Fatalf("expected --agent for the private memory scope: %v", args)
	}
}

func TestBridgeArgsOmitsScopeWhenUnset(t *testing.T) {
	s := NewSupervisor(context.Background(), "/bin/herrscher")
	args := s.bridgeArgs(state.Session{Name: "demo", ChannelID: "c1"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--project") || strings.Contains(joined, "--agent") {
		t.Fatalf("no scope flags expected when project/agent unset: %v", args)
	}
}

// TestRunLoopLogsRestartAsStructuredWarn drives one crash-restart cycle and
// asserts the restart line is a structured slog record (level=warn, session
// field) routed through the injected logger — not a raw fmt.Fprintf string.
func TestRunLoopLogsRestartAsStructuredWarn(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A binary that cannot start makes cmd.Run() return immediately, exercising
	// the restart path without spawning a real bridge.
	s := NewSupervisor(ctx, "/herrscher/does-not-exist")
	s.SetLogger(obs.NewLogger(&buf, slog.LevelDebug))
	// Cancel on the first backoff sleep so the loop logs exactly once then exits.
	s.sleep = func(time.Duration) <-chan time.Time {
		cancel()
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}

	s.runLoop(ctx, state.Session{Name: "demo"})

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("expected a warn-level restart record, got %q", out)
	}
	if !strings.Contains(out, "session=demo") {
		t.Fatalf("expected a session field on the restart record, got %q", out)
	}
}

// captureDelays runs runLoop against a never-starting binary, recording the
// delay handed to each restart and cancelling after wantN restarts. now controls
// the per-attempt clock so the test fixes how long each attempt "ran".
func captureDelays(t *testing.T, wantN int, now func() time.Time) []time.Duration {
	t.Helper()
	var delays []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSupervisor(ctx, "/herrscher/does-not-exist")
	s.SetLogger(obs.NewLogger(io.Discard, slog.LevelInfo))
	s.now = now
	s.sleep = func(d time.Duration) <-chan time.Time {
		delays = append(delays, d)
		if len(delays) >= wantN {
			cancel()
		}
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	s.runLoop(ctx, state.Session{Name: "demo"})
	return delays
}

// TestRunLoopBacksOffGeometrically asserts a tight crash loop (each attempt
// reports ~0 runtime via the frozen clock) produces strictly growing delays.
func TestRunLoopBacksOffGeometrically(t *testing.T) {
	frozen := time.Unix(0, 0)
	delays := captureDelays(t, 4, func() time.Time { return frozen })
	if len(delays) < 4 {
		t.Fatalf("expected at least 4 restart delays, got %v", delays)
	}
	pol := obs.RestartBackoff()
	for i := 1; i < len(delays); i++ {
		if delays[i-1] < pol.Max && delays[i] <= delays[i-1] {
			t.Fatalf("delay did not grow at %d: %v", i, delays)
		}
	}
}

// TestRunLoopResetsBackoffAfterHealthyRun asserts that when each attempt runs
// longer than resetAfter, the streak resets every time so the delay stays at
// base — proving the measured runtime feeds the backoff (not a constant 0).
func TestRunLoopResetsBackoffAfterHealthyRun(t *testing.T) {
	pol := obs.RestartBackoff()
	tick := time.Unix(0, 0)
	// Each now() call advances past the reset threshold, so start→end of every
	// attempt exceeds it.
	delays := captureDelays(t, 3, func() time.Time {
		tick = tick.Add(2 * pol.Reset)
		return tick
	})
	floor := time.Duration(float64(pol.Base) * (1 - pol.Jitter))
	for i, d := range delays {
		if d < floor || d > pol.Base {
			t.Fatalf("delay %d = %v, want within [%v, %v] (reset to base each time)", i, d, floor, pol.Base)
		}
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
