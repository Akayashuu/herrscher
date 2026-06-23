package metrics

import (
	"testing"
	"time"
)

func TestCountersAccumulate(t *testing.T) {
	r := NewRegistry()
	r.TurnStarted()
	r.TurnStarted()
	r.TurnCompleted()
	r.TurnAbandoned()
	r.BridgeRestart()
	r.RemoteAttempt()
	r.RemoteAttempt()
	r.RemoteFailure()

	s := r.Snapshot()
	if s.TurnsStarted != 2 || s.TurnsCompleted != 1 || s.TurnsAbandoned != 1 {
		t.Fatalf("turn counters wrong: %+v", s)
	}
	if s.BridgeRestarts != 1 {
		t.Fatalf("bridge restarts = %d, want 1", s.BridgeRestarts)
	}
	if s.RemoteAttempts != 2 || s.RemoteFailures != 1 {
		t.Fatalf("remote counters wrong: %+v", s)
	}
}

func TestRemoteLatencyPercentiles(t *testing.T) {
	r := NewRegistry()
	for _, ms := range []int64{10, 20, 30, 40, 50} {
		r.RemoteLatency(time.Duration(ms) * time.Millisecond)
	}
	s := r.Snapshot()
	if s.RemoteLatency.Count != 5 {
		t.Fatalf("latency count = %d, want 5", s.RemoteLatency.Count)
	}
	// Nearest-rank: p50 of 5 sorted samples is the 3rd (30), p95 is the 5th (50).
	if s.RemoteLatency.P50MS != 30 {
		t.Fatalf("p50 = %d, want 30", s.RemoteLatency.P50MS)
	}
	if s.RemoteLatency.P95MS != 50 {
		t.Fatalf("p95 = %d, want 50", s.RemoteLatency.P95MS)
	}
}

func TestEmptyLatencyIsZero(t *testing.T) {
	s := NewRegistry().Snapshot()
	if s.RemoteLatency.Count != 0 || s.RemoteLatency.P50MS != 0 || s.RemoteLatency.P95MS != 0 {
		t.Fatalf("empty latency should be zero, got %+v", s.RemoteLatency)
	}
}

// TestNilRegistryIsNoOp lets call sites hold an optional registry without nil
// guards at every increment.
func TestNilRegistryIsNoOp(t *testing.T) {
	var r *Registry
	r.TurnStarted()
	r.BridgeRestart()
	r.RemoteAttempt()
	r.RemoteFailure()
	r.RemoteLatency(time.Second)
	if got := r.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("nil registry snapshot should be zero, got %+v", got)
	}
}
