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
	"google.golang.org/grpc/credentials"
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

	// creds authenticates the remote dial. nil => plaintext loopback (the
	// single-host default); a non-nil mTLS credential makes off-loopback safe.
	creds credentials.TransportCredentials

	log     *slog.Logger
	metrics *metrics.Registry

	// Retry/timeout knobs and clock seams for the remote path. dialMemory is the
	// single-attempt dial (default dialRemoteMemoryOnce); tests inject a fake
	// transport and a fast clock so retries run without real wall-clock waits.
	retryAttempts    int
	retryBudget      time.Duration
	attemptTimeout   time.Duration
	dialMemory       func(context.Context, contracts.Plugin) (contracts.Memory, error)
	dialOrchestrator func(context.Context, contracts.Plugin) (contracts.Orchestrator, error)
	dialBackend      func(context.Context, contracts.Plugin) (contracts.Backend, error)
	now              func() time.Time
	sleep            func(time.Duration) <-chan time.Time
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
	r.dialOrchestrator = r.dialRemoteOrchestratorOnce
	r.dialBackend = r.dialRemoteBackendOnce
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

// SetCredentials installs the transport credentials the remote dial authenticates
// with. nil keeps plaintext loopback (the default); an mTLS credential is required
// to dial a category running on another host.
func (r *Resolver) SetCredentials(c credentials.TransportCredentials) {
	r.creds = c
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
			p := p
			return resolveRemoteWithRetry(r, ctx, contracts.CategoryMemory,
				func(c context.Context) (contracts.Memory, error) { return r.dialMemory(c, p) })
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, getenv)
		if err != nil {
			return nil, err
		}
		return p.Memory(ctx, cfg)
	}
	return nil, nil
}

// Orchestrator resolves a REMOTE orchestrator proxy when HERRSCHER_REMOTE names
// "orchestrator", reusing the same retry/timeout/metrics harness as Memory. It
// returns (nil, nil) when the category is not remote: the local orchestrator
// needs runtime state the resolver does not hold (the in-process Memory plus the
// session/scope/learn config bag), so the caller builds the local one itself.
func (r *Resolver) Orchestrator(ctx context.Context, plugins []contracts.Plugin) (contracts.Orchestrator, error) {
	if !r.isRemote(contracts.CategoryOrchestrator) {
		return nil, nil
	}
	for _, p := range plugins {
		if p.Orchestrator == nil {
			continue
		}
		p := p
		return resolveRemoteWithRetry(r, ctx, contracts.CategoryOrchestrator,
			func(c context.Context) (contracts.Orchestrator, error) { return r.dialOrchestrator(c, p) })
	}
	return nil, nil
}

// Backend resolves a REMOTE backend proxy when HERRSCHER_REMOTE names "backend",
// reusing the same retry/timeout/metrics harness. Like Orchestrator it returns
// (nil, nil) when the category is not remote, leaving the caller to build the
// local backend (which closes over model config the resolver does not hold).
// The remote proxy streams turn events over gRPC and surfaces a stream loss as a
// Respond error so the turn loop abandons the in-flight turn.
func (r *Resolver) Backend(ctx context.Context, plugins []contracts.Plugin) (contracts.Backend, error) {
	if !r.isRemote(contracts.CategoryBackend) {
		return nil, nil
	}
	for _, p := range plugins {
		if p.Backend == nil {
			continue
		}
		p := p
		return resolveRemoteWithRetry(r, ctx, contracts.CategoryBackend,
			func(c context.Context) (contracts.Backend, error) { return r.dialBackend(c, p) })
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

// resolveRemoteWithRetry dials a remote category's proxy, retrying transient
// failures on the Stage A2 backoff until either an attempt succeeds, the attempt
// cap is hit, the total budget elapses, or ctx is cancelled. It is generic over
// the port type so every remote category reuses the same retry/timeout/metrics
// machinery via its own one-shot dial; memory and orchestrator are the callers
// as of C2 (the streaming backend joins in C3).
func resolveRemoteWithRetry[T any](r *Resolver, ctx context.Context, cat contracts.Category, dial func(context.Context) (T, error)) (T, error) {
	var zero T
	start := r.now()
	bo := &obs.Backoff{Base: retryBackoffBase, Factor: 2, Max: retryBackoffMax, Jitter: 0.2}
	var lastErr error
	attempt := 0
	for {
		attempt++
		r.metrics.RemoteAttempt()
		attemptStart := r.now()
		actx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
		v, err := dial(actx)
		cancel()
		r.metrics.RemoteLatency(r.now().Sub(attemptStart))
		if err == nil {
			if attempt > 1 {
				r.log.Debug("remote resolve recovered", "category", cat, "attempt", attempt)
			}
			return v, nil
		}
		lastErr = err
		if attempt >= r.retryAttempts || r.now().Sub(start) >= r.retryBudget || ctx.Err() != nil {
			break
		}
		r.log.Debug("remote resolve attempt failed", "category", cat, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-r.sleep(bo.Next(0)):
		}
	}
	r.metrics.RemoteFailure()
	elapsed := r.now().Sub(start)
	r.log.Warn("remote resolve gave up", "category", cat, "attempts", attempt, "elapsed", elapsed, "err", lastErr)
	return zero, &RemoteResolveError{Category: cat, Attempts: attempt, Elapsed: elapsed, Err: lastErr}
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
			return transport.DialMemory(ctx, mems[0], r.creds)
		}
		select {
		case <-seen:
		case <-ctx.Done():
			return nil, fmt.Errorf("resolver: no remote memory announced: %w", ctx.Err())
		}
	}
}

// dialRemoteOrchestratorOnce is one remote-resolve attempt for the orchestrator:
// connect to NATS, watch for an orchestrator announcement, and dial its gRPC
// proxy. Same shape as dialRemoteMemoryOnce; ctx bounds the announcement wait.
func (r *Resolver) dialRemoteOrchestratorOnce(ctx context.Context, _ contracts.Plugin) (contracts.Orchestrator, error) {
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
		if orchs := reg.Orchestrators(); len(orchs) > 0 {
			return transport.DialOrchestrator(ctx, orchs[0], r.creds)
		}
		select {
		case <-seen:
		case <-ctx.Done():
			return nil, fmt.Errorf("resolver: no remote orchestrator announced: %w", ctx.Err())
		}
	}
}

// dialRemoteBackendOnce is one remote-resolve attempt for the backend: connect to
// NATS, watch for a backend announcement, and dial its streaming gRPC proxy. Same
// shape as the memory/orchestrator dials; ctx bounds the announcement wait.
func (r *Resolver) dialRemoteBackendOnce(ctx context.Context, _ contracts.Plugin) (contracts.Backend, error) {
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
		if bes := reg.Backends(); len(bes) > 0 {
			return transport.DialBackend(ctx, bes[0], r.creds)
		}
		select {
		case <-seen:
		case <-ctx.Done():
			return nil, fmt.Errorf("resolver: no remote backend announced: %w", ctx.Err())
		}
	}
}
