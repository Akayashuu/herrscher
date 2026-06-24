package host

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/health"
	"github.com/Herrscherd/herrscher/core/internal/obs"
)

// remotePlugin is a memory plugin whose factory is non-nil (so Memory enters the
// remote branch) but is never called on the remote path.
func remotePlugin() []contracts.Plugin {
	return []contracts.Plugin{{
		Manifest: contracts.Manifest{Category: contracts.CategoryMemory},
		Memory:   func(context.Context, contracts.PluginConfig) (contracts.Memory, error) { return nil, nil },
	}}
}

// fastClock wires the resolver's clock seams so retries incur no real waits.
func fastClock(r *Resolver, now func() time.Time) {
	r.now = now
	r.sleep = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

func TestRemoteResolveRetriesThenSucceeds(t *testing.T) {
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, "")
	frozen := time.Unix(0, 0)
	fastClock(r, func() time.Time { return frozen })

	want := &stubMem{}
	var calls int
	r.dialMemory = func(context.Context, contracts.Plugin) (contracts.Memory, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("transient")
		}
		return want, nil
	}

	got, err := r.Memory(context.Background(), remotePlugin(), func(string) string { return "" })
	if err != nil {
		t.Fatalf("expected success within the retry budget, got %v", err)
	}
	if got != contracts.Memory(want) {
		t.Fatalf("got %v, want the dialed memory", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts (2 fail, 1 succeed), got %d", calls)
	}
}

func TestRemoteResolveExhaustsAttemptsAndWarns(t *testing.T) {
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, "")
	frozen := time.Unix(0, 0)
	fastClock(r, func() time.Time { return frozen })
	var buf bytes.Buffer
	r.SetLogger(obs.NewLogger(&buf, slog.LevelDebug))

	var calls int
	r.dialMemory = func(context.Context, contracts.Plugin) (contracts.Memory, error) {
		calls++
		return nil, context.DeadlineExceeded
	}

	got, err := r.Memory(context.Background(), remotePlugin(), func(string) string { return "" })
	if got != nil {
		t.Fatalf("expected nil memory on give-up, got %v", got)
	}
	var rre *RemoteResolveError
	if !errors.As(err, &rre) {
		t.Fatalf("expected *RemoteResolveError, got %T: %v", err, err)
	}
	if calls != r.retryAttempts {
		t.Fatalf("expected %d attempts, got %d", r.retryAttempts, calls)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "category=memory") {
		t.Fatalf("expected a warn give-up record carrying the category, got %q", out)
	}
}

func TestRemoteResolveStopsAtBudget(t *testing.T) {
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, "")
	r.retryAttempts = 100 // high, so the budget — not the attempt cap — must stop it
	r.retryBudget = 10 * time.Second
	tick := time.Unix(0, 0)
	fastClock(r, func() time.Time { tick = tick.Add(6 * time.Second); return tick })

	var calls int
	r.dialMemory = func(context.Context, contracts.Plugin) (contracts.Memory, error) {
		calls++
		return nil, errors.New("down")
	}

	_, err := r.Memory(context.Background(), remotePlugin(), func(string) string { return "" })
	var rre *RemoteResolveError
	if !errors.As(err, &rre) {
		t.Fatalf("expected *RemoteResolveError, got %v", err)
	}
	if calls >= r.retryAttempts {
		t.Fatalf("budget should stop retries well before the attempt cap, got %d calls", calls)
	}
}

// TestRemoteResolveRecordsMetricsOnHealth wires the resolver into the same
// registry the health surface reports, so a failed remote resolve is visible on
// the health snapshot (attempts, one failure, and a latency sample).
func TestRemoteResolveRecordsMetricsOnHealth(t *testing.T) {
	h := health.NewHealth(time.Unix(0, 0))
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, "")
	r.SetMetrics(h.Metrics())
	frozen := time.Unix(0, 0)
	fastClock(r, func() time.Time { return frozen })
	r.dialMemory = func(context.Context, contracts.Plugin) (contracts.Memory, error) {
		return nil, context.DeadlineExceeded
	}

	_, _ = r.Memory(context.Background(), remotePlugin(), func(string) string { return "" })

	m := h.Snapshot(time.Unix(1, 0), 30*time.Second).Metrics
	if m.RemoteAttempts != int64(r.retryAttempts) {
		t.Fatalf("remote attempts = %d, want %d", m.RemoteAttempts, r.retryAttempts)
	}
	if m.RemoteFailures != 1 {
		t.Fatalf("remote failures = %d, want 1", m.RemoteFailures)
	}
	if m.RemoteLatency.Count < 1 {
		t.Fatalf("expected at least one remote latency sample, got %d", m.RemoteLatency.Count)
	}
}

// TestLocalResolveSkipsRetrySeam asserts the in-process path never touches the
// remote dial/retry seam — no retry/timeout overhead when nothing is remote.
func TestLocalResolveSkipsRetrySeam(t *testing.T) {
	r := NewResolver(nil, "") // nothing remote
	var calls int
	r.dialMemory = func(context.Context, contracts.Plugin) (contracts.Memory, error) {
		calls++
		return nil, nil
	}
	want := &stubMem{}
	_, err := r.Memory(context.Background(), []contracts.Plugin{{
		Manifest: contracts.Manifest{Category: contracts.CategoryMemory},
		Memory:   func(context.Context, contracts.PluginConfig) (contracts.Memory, error) { return want, nil },
	}}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("local resolve: %v", err)
	}
	if calls != 0 {
		t.Fatalf("local path must not call the remote retry seam, got %d calls", calls)
	}
}

// orchestratorPlugin is an orchestrator plugin whose factory is non-nil (so the
// remote branch is entered) but is never called on the remote path.
func orchestratorPlugin() []contracts.Plugin {
	return []contracts.Plugin{{
		Manifest: contracts.Manifest{Category: contracts.CategoryOrchestrator},
		Orchestrator: func(context.Context, contracts.PluginConfig, contracts.Memory) (contracts.Orchestrator, error) {
			return nil, nil
		},
	}}
}

// TestRemoteResolveOrchestratorRetriesThenSucceeds proves the orchestrator reuses
// the same Stage A retry harness as memory: a transient dial failure is retried
// within budget and the recovered proxy is returned.
func TestRemoteResolveOrchestratorRetriesThenSucceeds(t *testing.T) {
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryOrchestrator: true}, "")
	frozen := time.Unix(0, 0)
	fastClock(r, func() time.Time { return frozen })

	want := &recordingOrch{}
	var calls int
	r.dialOrchestrator = func(context.Context, contracts.Plugin) (contracts.Orchestrator, error) {
		calls++
		if calls <= 2 {
			return nil, errors.New("transient")
		}
		return want, nil
	}

	got, err := r.Orchestrator(context.Background(), orchestratorPlugin())
	if err != nil {
		t.Fatalf("expected success within the retry budget, got %v", err)
	}
	if got != contracts.Orchestrator(want) {
		t.Fatalf("got %v, want the dialed orchestrator", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts (2 fail, 1 succeed), got %d", calls)
	}
}

// TestRemoteResolveOrchestratorFailsCleanlyWithMetrics asserts a give-up returns
// a typed error (not a panic) and moves the same metrics counters memory does.
func TestRemoteResolveOrchestratorFailsCleanlyWithMetrics(t *testing.T) {
	h := health.NewHealth(time.Unix(0, 0))
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryOrchestrator: true}, "")
	r.SetMetrics(h.Metrics())
	frozen := time.Unix(0, 0)
	fastClock(r, func() time.Time { return frozen })
	r.dialOrchestrator = func(context.Context, contracts.Plugin) (contracts.Orchestrator, error) {
		return nil, context.DeadlineExceeded
	}

	got, err := r.Orchestrator(context.Background(), orchestratorPlugin())
	if got != nil {
		t.Fatalf("expected nil orchestrator on give-up, got %v", got)
	}
	var rre *RemoteResolveError
	if !errors.As(err, &rre) {
		t.Fatalf("expected *RemoteResolveError, got %T: %v", err, err)
	}
	if rre.Category != contracts.CategoryOrchestrator {
		t.Fatalf("error category = %q, want orchestrator", rre.Category)
	}
	m := h.Snapshot(time.Unix(1, 0), 30*time.Second).Metrics
	if m.RemoteAttempts != int64(r.retryAttempts) || m.RemoteFailures != 1 {
		t.Fatalf("metrics did not move: attempts=%d failures=%d", m.RemoteAttempts, m.RemoteFailures)
	}
}

// TestLocalOrchestratorResolveSkipsDial is the C2 "zero dials when local" check:
// without orchestrator in HERRSCHER_REMOTE, Orchestrator returns (nil, nil) and
// never touches the dial seam, leaving buildOrchestrator to build in-process.
func TestLocalOrchestratorResolveSkipsDial(t *testing.T) {
	r := NewResolver(nil, "") // nothing remote
	var calls int
	r.dialOrchestrator = func(context.Context, contracts.Plugin) (contracts.Orchestrator, error) {
		calls++
		return nil, nil
	}
	orch, err := r.Orchestrator(context.Background(), orchestratorPlugin())
	if err != nil {
		t.Fatalf("local resolve: %v", err)
	}
	if orch != nil {
		t.Fatalf("Orchestrator must return (nil, nil) on the local path, got %v", orch)
	}
	if calls != 0 {
		t.Fatalf("local path must not dial the remote orchestrator, got %d calls", calls)
	}
}

// TestLocalBackendResolveSkipsDial is the C3 "zero dials when local" check:
// without backend in HERRSCHER_REMOTE, Backend returns (nil, nil) and never
// dials, leaving the bridge to build the in-process backend.
func TestLocalBackendResolveSkipsDial(t *testing.T) {
	r := NewResolver(nil, "") // nothing remote
	var calls int
	r.dialBackend = func(context.Context, contracts.Plugin) (contracts.Backend, error) {
		calls++
		return nil, nil
	}
	be, err := r.Backend(context.Background(), []contracts.Plugin{{
		Manifest: contracts.Manifest{Category: contracts.CategoryBackend},
		Backend:  func(context.Context, contracts.PluginConfig) (contracts.Backend, error) { return nil, nil },
	}})
	if err != nil {
		t.Fatalf("local resolve: %v", err)
	}
	if be != nil {
		t.Fatalf("Backend must return (nil, nil) on the local path, got %v", be)
	}
	if calls != 0 {
		t.Fatalf("local path must not dial the remote backend, got %d calls", calls)
	}
}
