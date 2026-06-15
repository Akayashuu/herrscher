package session

import "testing"

// TestTmuxSessionExistsUnknown verifies a clearly-absent session reports false
// without panicking, even when tmux is not installed (tmuxRun returns an error).
func TestTmuxSessionExistsUnknown(t *testing.T) {
	if TmuxSessionExists("dctl-this-channel-does-not-exist-zzz") {
		t.Fatal("expected false for a non-existent tmux session")
	}
}
