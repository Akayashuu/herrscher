package control_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
)

func TestConnRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rt.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()

	cli, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	srvConn := <-acc.Conns()

	// daemon → bridge
	if err := srvConn.Write(contracts.Event{T: "input", Who: "alice", Text: "hi"}); err != nil {
		t.Fatalf("server Write: %v", err)
	}
	var got contracts.Event
	done := make(chan struct{})
	go func() {
		_ = cli.Scan(func(e contracts.Event) error { got = e; close(done); return errStop })
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for input frame")
	}
	if got.T != "input" || got.Text != "hi" || got.Who != "alice" {
		t.Fatalf("client got %+v, want input/alice/hi", got)
	}
}

var errStop = stopErr{}

type stopErr struct{}

func (stopErr) Error() string { return "stop" }

func TestConnConcurrentWrites(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "cw.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()
	cli, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()
	srvConn := <-acc.Conns()

	const n = 50
	var got int
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		_ = srvConn.Scan(func(contracts.Event) error {
			mu.Lock()
			got++
			reached := got == n
			mu.Unlock()
			if reached {
				close(done)
				return errStop
			}
			return nil
		})
	}()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = cli.Write(contracts.Event{T: "chunk", Text: "x"}) }()
	}
	wg.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("got %d events, want %d (interleaved write corruption?)", got, n)
	}
}

func TestAcceptorYieldsSecondConnAfterFirstCloses(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "rc.sock")
	acc, err := control.Accept(sock)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer acc.Close()

	c1, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial 1: %v", err)
	}
	<-acc.Conns()
	c1.Close() // simulate a bridge crash

	c2, err := control.Dial(sock)
	if err != nil {
		t.Fatalf("Dial 2 (reconnect): %v", err)
	}
	defer c2.Close()
	select {
	case sc := <-acc.Conns():
		if sc == nil {
			t.Fatal("second conn is nil")
		}
	case <-time.After(time.Second):
		t.Fatal("acceptor did not yield a second conn after reconnect")
	}
}
