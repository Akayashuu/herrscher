package session

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestStreamSessionLiveTwoTurns exercises the real persistent-process path:
// one claude stream-json process answering two sequential Send calls (as the
// bridge does). Skipped unless DCTL_LIVE=1 (it spends real model quota).
func TestStreamSessionLiveTwoTurns(t *testing.T) {
	if os.Getenv("DCTL_LIVE") != "1" {
		t.Skip("set DCTL_LIVE=1 to run the live claude smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := startStreamSession(ctx, []string{"claude"}, "claude-haiku-4-5-20251001", "", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tr1, err := s.Send("Reply with exactly one word: ONE", nil)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn1: text=%q session=%s cost=$%.4f", tr1.Text, tr1.SessionID, tr1.CostUSD)

	tr2, err := s.Send("Reply with exactly one word: TWO", nil)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	t.Logf("turn2: text=%q session=%s cost=$%.4f", tr2.Text, tr2.SessionID, tr2.CostUSD)

	if tr1.SessionID == "" || tr1.SessionID != tr2.SessionID {
		t.Fatalf("expected one persistent session across turns, got %q then %q", tr1.SessionID, tr2.SessionID)
	}
	if tr1.Text == "" || tr2.Text == "" {
		t.Fatal("both turns should return non-empty text")
	}
}
