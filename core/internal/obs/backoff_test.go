package obs

import (
	"testing"
	"time"
)

// noJitter makes Next deterministic: r()=0 ⇒ delay is the full base*factor^n.
func noJitter() float64 { return 0 }

func TestBackoffGrowsGeometricallyToCap(t *testing.T) {
	b := &Backoff{Base: 100 * time.Millisecond, Factor: 2, Max: time.Second, Reset: time.Minute, Rand: noJitter}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second, // capped (would be 1600ms)
		time.Second, // stays at cap
	}
	for i, w := range want {
		if got := b.Next(0); got != w {
			t.Fatalf("attempt %d: delay = %v, want %v", i, got, w)
		}
	}
}

func TestBackoffResetsAfterHealthyRun(t *testing.T) {
	b := &Backoff{Base: 100 * time.Millisecond, Factor: 2, Max: time.Second, Reset: time.Minute, Rand: noJitter}
	b.Next(0) // 100ms, n→1
	b.Next(0) // 200ms, n→2
	b.Next(0) // 400ms, n→3
	// A run that lasted past the reset threshold returns the next delay to base.
	if got := b.Next(2 * time.Minute); got != 100*time.Millisecond {
		t.Fatalf("after healthy run, delay = %v, want base 100ms", got)
	}
	// And growth resumes from base.
	if got := b.Next(0); got != 200*time.Millisecond {
		t.Fatalf("post-reset growth: delay = %v, want 200ms", got)
	}
}

func TestBackoffJitterStaysInBounds(t *testing.T) {
	const jitter = 0.5
	for _, r := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		b := &Backoff{Base: time.Second, Factor: 2, Max: time.Minute, Jitter: jitter, Rand: func() float64 { return r }}
		// n=0 ⇒ uncapped target = base = 1s. Jittered into [base*(1-j), base].
		got := b.Next(0)
		lo := time.Duration(float64(time.Second) * (1 - jitter))
		hi := time.Second
		if got < lo || got > hi {
			t.Fatalf("r=%v: delay %v out of [%v, %v]", r, got, lo, hi)
		}
	}
}
