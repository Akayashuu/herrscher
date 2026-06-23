package health

import (
	"testing"
	"time"
)

func TestHealthSnapshotOnline(t *testing.T) {
	h := NewHealth(time.Unix(1000, 0))
	h.HeartbeatAck(time.Unix(1005, 0))
	h.SetSessions(2)
	snap := h.Snapshot(time.Unix(1010, 0), 30*time.Second)
	if !snap.Online {
		t.Fatal("expected online (heartbeat within window)")
	}
	if snap.Sessions != 2 || snap.UptimeS != 10 {
		t.Fatalf("snap wrong: %+v", snap)
	}
}

func TestHealthGoesOfflineWhenHeartbeatStale(t *testing.T) {
	h := NewHealth(time.Unix(1000, 0))
	h.HeartbeatAck(time.Unix(1005, 0))
	snap := h.Snapshot(time.Unix(2000, 0), 30*time.Second) // 995s since last ack
	if snap.Online {
		t.Fatal("expected offline (heartbeat stale)")
	}
}

func TestHealthOfflineWithoutHeartbeat(t *testing.T) {
	h := NewHealth(time.Unix(1000, 0))
	snap := h.Snapshot(time.Unix(1001, 0), 30*time.Second)
	if snap.Online {
		t.Fatal("no heartbeat yet → offline")
	}
}

func TestHealthSnapshotReportsMetrics(t *testing.T) {
	h := NewHealth(time.Unix(1000, 0))
	m := h.Metrics()
	m.TurnStarted()
	m.TurnCompleted()
	m.BridgeRestart()
	m.RemoteAttempt()
	m.RemoteFailure()

	snap := h.Snapshot(time.Unix(1001, 0), 30*time.Second)
	got := snap.Metrics
	if got.TurnsStarted != 1 || got.TurnsCompleted != 1 {
		t.Fatalf("turn metrics not surfaced: %+v", got)
	}
	if got.BridgeRestarts != 1 || got.RemoteAttempts != 1 || got.RemoteFailures != 1 {
		t.Fatalf("metrics not surfaced on the health snapshot: %+v", got)
	}
}
