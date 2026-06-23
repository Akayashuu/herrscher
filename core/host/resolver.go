package host

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/Herrscherd/herrscher/core/internal/metrics"
	"github.com/Herrscherd/herrscher/core/internal/obs"
	"github.com/nats-io/nats.go"
)

// Remote-resolve retry/timeout defaults. They bound a remote dial so a slow or
// briefly-unavailable dependency is retried within a deadline instead of
// blocking or killing the session — and apply only on the remote path.
const (
	defaultRetryAttempts  = 5
	defaultRetryBudget    = 20 * time.Second
	defaultAttemptTimeout = 5 * time.Second
	retryBackoffBase      = 200 * time.Millisecond
	retryBackoffMax       = 2 * time.Second
)

// Resolver turns registered plugins into live port objects, choosing local
// (in-proc factory) or remote (gRPC proxy) per category. With a nil/empty
// remote set every category resolves local — today's behaviour.
type Resolver struct {
	remote  map[contracts.Category]bool
	NatsURL string // "" => nats.DefaultURL; consulted only on the remote path

	log     *slog.Logger
	metrics *metrics.Registry

	// Retry/timeout knobs and clock seams for the remote path. dialMemory is the
	// single-attempt dial (default dialRemoteMemoryOnce); tests inject a fake
	// transport and a fast clock so retries run without real wall-clock waits.
	retryAttempts  int
	retryBudget    time.Duration
	attemptTimeout time.Duration
	dialMemory     func(context.Context, contracts.Plugin) (contracts.Memory, error)
	now            func() time.Time
	sleep          func(time.Duration) <-chan time.Time
}

func NewResolver(remote map[contracts.Category]bool, natsURL string) *Resolver {
	r := &Resolver{
		remote:         remote,
		NatsURL:        natsURL,
		log:            obs.NewLogger(os.Stderr, slog.LevelInfo),
		retryAttempts:  defaultRetryAttempts,
		retryBudget:    defaultRetryBudget,
		attemptTimeout: defaultAttemptTimeout,
		now:            time.Now,
		sleep:          time.After,
	}
	r.dialMemory = r.dialRemoteMemoryOnce
	return r
}

// SetLogger installs the operator logger remote-resolve diagnostics flow through
// (component=resolver is attached for filtering).
func (r *Resolver) SetLogger(l *slog.Logger) {
	r.log = l.With("component", "resolver")
}

// SetMetrics installs the registry remote-resolve attempts/failures/latency are
// recorded into.
func (r *Resolver) SetMetrics(m *metrics.Registry) {
	r.metrics = m
}

func (r *Resolver) isRemote(c contracts.Category) bool {
	return r.remote != nil && r.remote[c]
}

// Memory resolves the first registered memory plugin. Local: call the factory.
// Remote: dial a gRPC proxy via NATS announcements, retried within a deadline.
// Returns nil (no error) when none is registered — memory stays optional,
// matching buildMemory's contract.
func (r *Resolver) Memory(ctx context.Context, plugins []contracts.Plugin, getenv func(string) string) (contracts.Memory, error) {
	for _, p := range plugins {
		if p.Memory == nil {
			continue
		}
		if r.isRemote(contracts.CategoryMemory) {
			return r.resolveMemoryWithRetry(ctx, p)
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, getenv)
		if err != nil {
			return nil, err
		}
		return p.Memory(ctx, cfg)
	}
	return nil, nil
}

// RemoteResolveError reports that a remote category could not be resolved within
// the retry budget. It carries the category, attempts made, and elapsed time so
// the caller can degrade cleanly; Unwrap exposes the last underlying error.
type RemoteResolveError struct {
	Category contracts.Category
	Attempts int
	Elapsed  time.Duration
	Err      error
}

func (e *RemoteResolveError) Error() string {
	return fmt.Sprintf("resolver: remote %s unavailable after %d attempt(s) in %s: %v",
		e.Category, e.Attempts, e.Elapsed, e.Err)
}

func (e *RemoteResolveError) Unwrap() error { return e.Err }

// resolveMemoryWithRetry dials the remote memory proxy, retrying transient
// failures on the Stage A2 backoff until either an attempt succeeds, the attempt
// cap is hit, the total budget elapses, or ctx is cancelled.
func (r *Resolver) resolveMemoryWithRetry(ctx context.Context, p contracts.Plugin) (contracts.Memory, error) {
	start := r.now()
	bo := &obs.Backoff{Base: retryBackoffBase, Factor: 2, Max: retryBackoffMax, Jitter: 0.2}
	var lastErr error
	attempt := 0
	for {
		attempt++
		r.metrics.RemoteAttempt()
		attemptStart := r.now()
		actx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
		mem, err := r.dialMemory(actx, p)
		cancel()
		r.metrics.RemoteLatency(r.now().Sub(attemptStart))
		if err == nil {
			if attempt > 1 {
				r.log.Debug("remote resolve recovered", "category", contracts.CategoryMemory, "attempt", attempt)
			}
			return mem, nil
		}
		lastErr = err
		if attempt >= r.retryAttempts || r.now().Sub(start) >= r.retryBudget || ctx.Err() != nil {
			break
		}
		r.log.Debug("remote resolve attempt failed", "category", contracts.CategoryMemory, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-r.sleep(bo.Next(0)):
		}
	}
	r.metrics.RemoteFailure()
	elapsed := r.now().Sub(start)
	r.log.Warn("remote resolve gave up", "category", contracts.CategoryMemory, "attempts", attempt, "elapsed", elapsed, "err", lastErr)
	return nil, &RemoteResolveError{Category: contracts.CategoryMemory, Attempts: attempt, Elapsed: elapsed, Err: lastErr}
}

func (r *Resolver) natsURL() string {
	if r.NatsURL != "" {
		return r.NatsURL
	}
	return nats.DefaultURL
}

// dialRemoteMemoryOnce is one remote-resolve attempt: connect to NATS, watch for
// a memory announcement, and dial its gRPC proxy. The announcement wait and dial
// are bounded by ctx (the caller sets the per-attempt deadline).
func (r *Resolver) dialRemoteMemoryOnce(ctx context.Context, _ contracts.Plugin) (contracts.Memory, error) {
	nc, err := nats.Connect(r.natsURL())
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	reg := transport.NewRemoteRegistry()
	seen := make(chan struct{}, 1)
	if err := transport.WatchAnnouncements(nc, func(a transport.Announcement) {
		reg.Observe(a)
		select {
		case seen <- struct{}{}:
		default:
		}
	}); err != nil {
		return nil, err
	}
	for {
		if mems := reg.Memories(); len(mems) > 0 {
			return transport.DialMemory(ctx, mems[0])
		}
		select {
		case <-seen:
		case <-ctx.Done():
			return nil, fmt.Errorf("resolver: no remote memory announced: %w", ctx.Err())
		}
	}
}
