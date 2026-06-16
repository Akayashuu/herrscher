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
