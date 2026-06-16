package control

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSocketPathDeterministicAndSafe(t *testing.T) {
	a := SocketPath("my session.1")
	b := SocketPath("my session.1")
	if a != b {
		t.Fatalf("SocketPath not deterministic: %q vs %q", a, b)
	}
	base := filepath.Base(a)
	stem := strings.TrimSuffix(strings.TrimPrefix(base, "dctl-control-"), ".sock")
	if stem == base || strings.ContainsAny(stem, " .") {
		t.Fatalf("unsafe socket filename: %q (stem %q)", base, stem)
	}
}

func TestSendReceiveRoundTrip(t *testing.T) {
	// Use a short temp path; a long TempDir could exceed the unix socket limit.
	path := filepath.Join(t.TempDir(), "c.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if err := Send(path, "2"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-srv.Values():
		if got != "2" {
			t.Fatalf("received %q, want 2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for value")
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	s1, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	// A second Listen on the same path must succeed (stale file removed).
	s2, err := Listen(path)
	if err != nil {
		t.Fatalf("re-listen after close failed: %v", err)
	}
	s2.Close()
}

func TestValuesClosesOnServerClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	select {
	case _, ok := <-srv.Values():
		if ok {
			t.Fatal("expected Values channel to be closed after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Values channel did not close after server Close")
	}
}
