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
