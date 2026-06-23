package host

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/metrics"
)

// TestTurnMetricsCountStartCompleteAbandon drives the turn seam through a
// completed turn (reply{done}) and an abandoned one (hangup) and asserts the
// counters move accordingly.
func TestTurnMetricsCountStartCompleteAbandon(t *testing.T) {
	a := &fanRecorder{}
	toBridge := make(chan contracts.Event, 4)
	fromBridge := make(chan contracts.Event, 4)
	d := newSessionDriver("s1", []contracts.GatewaySet{{Gateway: a, Reader: a}}, toBridge, fromBridge)
	m := metrics.NewRegistry()
	d.metrics = m

	// A turn that completes: the bridge emits reply{done}.
	fromBridge <- contracts.Event{T: "reply", Text: "ok", Done: true}
	d.awaitTurn(context.Background())

	// A turn that is abandoned: the bridge connection hangs up mid-turn.
	d.hangup <- struct{}{}
	d.awaitTurn(context.Background())

	s := m.Snapshot()
	if s.TurnsStarted != 2 {
		t.Fatalf("turns started = %d, want 2", s.TurnsStarted)
	}
	if s.TurnsCompleted != 1 {
		t.Fatalf("turns completed = %d, want 1", s.TurnsCompleted)
	}
	if s.TurnsAbandoned != 1 {
		t.Fatalf("turns abandoned = %d, want 1", s.TurnsAbandoned)
	}
}
