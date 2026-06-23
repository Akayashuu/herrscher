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
