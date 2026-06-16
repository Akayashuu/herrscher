package main

import "testing"

// TestBridgeParticipantsFlagWired is a compile-time guard: it fails to build until
// bridge.Options gains a Participants field, ensuring runBridge can set it.
func TestBridgeParticipantsFlagWired(t *testing.T) {
	_ = bridgeOptionsHasParticipants
}
