package host

import (
	"context"
	"net"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

func TestResolverRemoteMemoryDials(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	transport.RegisterMemorySkeleton(gs, recordingMem{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	ann := transport.Announcement{
		Manifest:   contracts.Manifest{Kind: "sqlite", Category: contracts.CategoryMemory},
		GrpcAddr:   lis.Addr().String(),
		InstanceID: "memory-0",
	}
	stop := make(chan struct{})
	go func() {
		for {
			_ = transport.Announce(nc, ann)
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { close(stop) })

	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, srv.ClientURL())
	mem, err := r.Memory(context.Background(),
		[]contracts.Plugin{{Manifest: contracts.Manifest{Category: contracts.CategoryMemory},
			Memory: func(context.Context, contracts.PluginConfig) (contracts.Memory, error) { return nil, nil }}},
		func(string) string { return "" })
	if err != nil {
		t.Fatalf("remote resolve: %v", err)
	}
	if err := mem.Record(context.Background(), contracts.Node{Key: "k"}); err != nil {
		t.Fatalf("proxy Record over remote: %v", err)
	}
}

type recordingMem struct{ contracts.Memory }

func (recordingMem) Record(context.Context, contracts.Node) error { return nil }
func (recordingMem) Close() error                                 { return nil }

func TestResolverRemoteOrchestratorDials(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	transport.RegisterOrchestratorSkeleton(gs, recordingOrch{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	ann := transport.Announcement{
		Manifest:   contracts.Manifest{Kind: "basic", Category: contracts.CategoryOrchestrator},
		GrpcAddr:   lis.Addr().String(),
		InstanceID: "orchestrator-0",
	}
	stop := make(chan struct{})
	go func() {
		for {
			_ = transport.Announce(nc, ann)
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { close(stop) })

	r := NewResolver(map[contracts.Category]bool{contracts.CategoryOrchestrator: true}, srv.ClientURL())
	orch, err := r.Orchestrator(context.Background(),
		[]contracts.Plugin{{Manifest: contracts.Manifest{Category: contracts.CategoryOrchestrator},
			Orchestrator: func(context.Context, contracts.PluginConfig, contracts.Memory) (contracts.Orchestrator, error) {
				return nil, nil
			}}})
	if err != nil {
		t.Fatalf("remote resolve: %v", err)
	}
	// The turn-shaping call round-trips over gRPC: Context returns the remote
	// value, and Observe surfaces no error.
	if got := orch.Context(context.Background()); got != "remote-context" {
		t.Fatalf("Context over remote = %q, want %q", got, "remote-context")
	}
	if err := orch.Observe(context.Background(), contracts.Prompt{Author: "u", Content: "hi"}, "ok"); err != nil {
		t.Fatalf("proxy Observe over remote: %v", err)
	}
}

type recordingOrch struct{ contracts.Orchestrator }

func (recordingOrch) Context(context.Context) string                          { return "remote-context" }
func (recordingOrch) Observe(context.Context, contracts.Prompt, string) error { return nil }
func (recordingOrch) Consolidate(context.Context) error                       { return nil }
func (recordingOrch) Close() error                                            { return nil }

func TestResolverRemoteBackendStreamsTurn(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	transport.RegisterBackendSkeleton(gs, streamingBackend{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	ann := transport.Announcement{
		Manifest:   contracts.Manifest{Kind: "claude", Category: contracts.CategoryBackend},
		GrpcAddr:   lis.Addr().String(),
		InstanceID: "backend-0",
	}
	stop := make(chan struct{})
	go func() {
		for {
			_ = transport.Announce(nc, ann)
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { close(stop) })

	r := NewResolver(map[contracts.Category]bool{contracts.CategoryBackend: true}, srv.ClientURL())
	be, err := r.Backend(context.Background(),
		[]contracts.Plugin{{Manifest: contracts.Manifest{Category: contracts.CategoryBackend},
			Backend: func(context.Context, contracts.PluginConfig) (contracts.Backend, error) { return nil, nil }}})
	if err != nil {
		t.Fatalf("remote resolve: %v", err)
	}

	// The turn streams over gRPC: the bridge sees the same event sequence a local
	// backend would, in order, then the terminal reply.
	var kinds []string
	reply, err := be.Respond(context.Background(), contracts.Prompt{Content: "hi"},
		func(e contracts.BackendEvent) { kinds = append(kinds, e.Kind) })
	if err != nil {
		t.Fatalf("remote Respond: %v", err)
	}
	if reply != "done reply" {
		t.Fatalf("reply = %q, want %q", reply, "done reply")
	}
	if want := []string{"tool", "text", "result"}; !equalStrings(kinds, want) {
		t.Fatalf("event kinds = %v, want %v (in order)", kinds, want)
	}
}

// streamingBackend is a fake backend that emits a fixed multi-event turn ending
// in a reply, exercising the streaming proxy's ordering + done boundary.
type streamingBackend struct{ contracts.Backend }

func (streamingBackend) Respond(_ context.Context, _ contracts.Prompt, onEvent func(contracts.BackendEvent)) (string, error) {
	onEvent(contracts.BackendEvent{Kind: "tool", Tool: "Read"})
	onEvent(contracts.BackendEvent{Kind: "text", Detail: "thinking"})
	onEvent(contracts.BackendEvent{Kind: "result", Cost: 0.01})
	return "done reply", nil
}
func (streamingBackend) Close() error { return nil }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
