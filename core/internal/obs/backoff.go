package obs

import (
	"math"
	"math/rand"
	"time"
)

// Backoff computes restart delays that grow geometrically (base·factorⁿ, capped
// at Max) with full jitter, and reset to base after a run that lasted at least
// Reset — so a process that ran healthily restarts fast while a tight crash loop
// backs off. The zero value is unusable; set at least Base and Factor.
type Backoff struct {
	Base   time.Duration // first delay (n=0)
	Max    time.Duration // upper cap before jitter; 0 ⇒ uncapped
	Factor float64       // geometric growth per consecutive failure
	Jitter float64       // jitter fraction in [0,1]: delay ∈ [target·(1-Jitter), target]
	Reset  time.Duration // a run ≥ Reset returns the next delay to Base; 0 ⇒ never

	// Rand draws the jitter sample in [0,1); nil falls back to math/rand.
	Rand func() float64

	n int // consecutive failures since the last reset
}

// RestartBackoff is the shared restart-loop policy: 1s base, ×2 growth capped at
// 30s, ±20% jitter, resetting to base after a 60s healthy run. The supervisor
// and the remote plugin-host loop both restart on this schedule.
func RestartBackoff() *Backoff {
	return &Backoff{Base: time.Second, Max: 30 * time.Second, Factor: 2, Jitter: 0.2, Reset: time.Minute}
}

// Next returns the delay before the next restart, given how long the attempt
// that just failed ran (ranFor). A ranFor ≥ Reset clears the failure streak so
// the delay returns to Base.
func (b *Backoff) Next(ranFor time.Duration) time.Duration {
	if b.Reset > 0 && ranFor >= b.Reset {
		b.n = 0
	}
	target := float64(b.Base) * math.Pow(b.Factor, float64(b.n))
	if ceiling := float64(b.Max); ceiling > 0 && target > ceiling {
		target = ceiling
	}
	b.n++
	draw := b.Rand
	if draw == nil {
		draw = rand.Float64
	}
	return time.Duration(target * (1 - b.Jitter*draw()))
}
