// Package metrics is a lightweight in-process metrics registry (atomic counters
// plus a bounded latency reservoir, stdlib only). It is surfaced through the
// existing health snapshot rather than a separate metrics stack. A nil *Registry
// is a no-op, so call sites can hold an optional registry without nil guards.
package metrics

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// reservoirCap bounds the retained latency samples; percentiles are computed over
// the most recent reservoirCap observations.
const reservoirCap = 256

// Registry accumulates runtime counters and remote-call latencies. Safe for
// concurrent use; the zero value is unusable — use NewRegistry.
type Registry struct {
	turnsStarted   atomic.Int64
	turnsCompleted atomic.Int64
	turnsAbandoned atomic.Int64
	bridgeRestarts atomic.Int64
	remoteAttempts atomic.Int64
	remoteFailures atomic.Int64

	mu       sync.Mutex
	latency  []int64 // bounded ring of remote-call latency samples (ms)
	latCount int64   // total observed (may exceed len(latency))
	latNext  int     // next ring slot to overwrite once full
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) TurnStarted() {
	if r != nil {
		r.turnsStarted.Add(1)
	}
}

func (r *Registry) TurnCompleted() {
	if r != nil {
		r.turnsCompleted.Add(1)
	}
}

func (r *Registry) TurnAbandoned() {
	if r != nil {
		r.turnsAbandoned.Add(1)
	}
}

func (r *Registry) BridgeRestart() {
	if r != nil {
		r.bridgeRestarts.Add(1)
	}
}

func (r *Registry) RemoteAttempt() {
	if r != nil {
		r.remoteAttempts.Add(1)
	}
}

func (r *Registry) RemoteFailure() {
	if r != nil {
		r.remoteFailures.Add(1)
	}
}

// RemoteLatency records one remote-call round-trip into the bounded reservoir.
func (r *Registry) RemoteLatency(d time.Duration) {
	if r == nil {
		return
	}
	ms := d.Milliseconds()
	r.mu.Lock()
	if len(r.latency) < reservoirCap {
		r.latency = append(r.latency, ms)
	} else {
		r.latency[r.latNext] = ms
		r.latNext = (r.latNext + 1) % reservoirCap
	}
	r.latCount++
	r.mu.Unlock()
}

// Latency summarizes the remote-call latency reservoir.
type Latency struct {
	Count int64 `json:"count"`
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
}

// Snapshot is an immutable view of the registry, embedded in the health surface.
type Snapshot struct {
	TurnsStarted   int64   `json:"turns_started"`
	TurnsCompleted int64   `json:"turns_completed"`
	TurnsAbandoned int64   `json:"turns_abandoned"`
	BridgeRestarts int64   `json:"bridge_restarts"`
	RemoteAttempts int64   `json:"remote_attempts"`
	RemoteFailures int64   `json:"remote_failures"`
	RemoteLatency  Latency `json:"remote_latency"`
}

func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	samples := append([]int64(nil), r.latency...)
	count := r.latCount
	r.mu.Unlock()
	return Snapshot{
		TurnsStarted:   r.turnsStarted.Load(),
		TurnsCompleted: r.turnsCompleted.Load(),
		TurnsAbandoned: r.turnsAbandoned.Load(),
		BridgeRestarts: r.bridgeRestarts.Load(),
		RemoteAttempts: r.remoteAttempts.Load(),
		RemoteFailures: r.remoteFailures.Load(),
		RemoteLatency:  Latency{Count: count, P50MS: percentile(samples, 50), P95MS: percentile(samples, 95)},
	}
}

// percentile returns the nearest-rank p-th percentile (ms) of samples, 0 if empty.
func percentile(samples []int64, p int) int64 {
	if len(samples) == 0 {
		return 0
	}
	slices.Sort(samples)
	rank := (p*len(samples) + 99) / 100 // ceil(p/100 * n)
	rank = max(rank, 1)
	rank = min(rank, len(samples))
	return samples[rank-1]
}
