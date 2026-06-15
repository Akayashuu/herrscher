package main

import "testing"

// TestBridgeParticipantsFlagWired is a compile-time guard: it fails to build until
// bridge.Options gains a Participants field, ensuring runBridge can set it.
func TestBridgeParticipantsFlagWired(t *testing.T) {
	_ = bridgeOptionsHasParticipants
}

// TestBridgeBackendFlagWired fails to build until bridge.Options gains a Backend
// field, ensuring runBridge can set it.
func TestBridgeBackendFlagWired(t *testing.T) {
	_ = bridgeOptionsHasBackend
}
