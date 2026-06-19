# NATS/gRPC Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a Herrscher plugin run as a separate process and be reached over a generic JSON-over-gRPC transport (announced via NATS), starting with the `memory` port, while the umbrella stays 100% functional in all-local mode.

**Architecture:** A new `herrscher-transport` module holds a generic gRPC `Call` service, a JSON codec, a client proxy + server skeleton for `contracts.Memory`, and a NATS announce client + remote registry. The host gains a `resolver` indirection that returns a `contracts.Memory` whether the plugin is in-proc (today's factory) or remote (a gRPC proxy). In-proc stays the default; remote is config-gated per category. This plan covers migration steps 1–3 (module, resolver, memory-remote). Steps 4–5 (backend/gateway/orchestrator, multi-machine) are follow-on plans.

**Tech Stack:** Go 1.25, `google.golang.org/grpc` + `google.golang.org/protobuf`, `github.com/nats-io/nats.go` (client) + `github.com/nats-io/nats-server/v2` (embedded server, test-only), `encoding/json` codec. Sole domain dependency: `github.com/Herrscherd/herrscher-contracts`.

**Reference spec:** `docs/superpowers/specs/2026-06-19-nats-grpc-transport-design.md`

---

## File Structure

New module **`herrscher-transport`** (separate repo `github.com/Herrscherd/herrscher-transport`, sibling to the other plugins; added to `/home/shan/dev/go.work`):

- `go.mod` — module `github.com/Herrscherd/herrscher-transport`, requires contracts + grpc + protobuf + nats.go (+ nats-server in test).
- `proto/transport.proto` — the generic `Plugin` service + `MethodEnvelope`/`ResultEnvelope`.
- `proto/transport.pb.go`, `proto/transport_grpc.pb.go` — generated; committed.
- `codec.go` — `Marshal(v any) ([]byte, error)` / `Unmarshal(data []byte, v any) error` over `encoding/json`; the single encode/decode point.
- `envelope.go` — helpers to build/read `MethodEnvelope`/`ResultEnvelope` and carry an error string across the wire.
- `memory_proxy.go` — `MemoryProxy` (client): implements `contracts.Memory`, marshals each method's args, issues `Call`, unmarshals the result.
- `memory_skeleton.go` — `RegisterMemorySkeleton(s *grpc.Server, real contracts.Memory)`: the gRPC server dispatch that routes `Call` → the real object's method.
- `announce.go` — `Announcement` struct (`Manifest`, `GrpcAddr`, `InstanceID`), `Announce(nc, ann)` (publish on `plugins.announce`), `WatchAnnouncements(nc, fn)` (subscribe).
- `remote_registry.go` — `RemoteRegistry`: accumulates announcements; same query surface as `contracts.Registry` (`Memories()` etc. returning `Plugin`-shaped entries whose factory dials a proxy).
- `subjects.go` — subject constants: `SubjectAnnounce = "plugins.announce"`, `SessionEvents(name)`.
- `*_test.go` — per-file unit tests; an integration test spins an embedded NATS + a real in-proc gRPC server.

Host changes in **`herrscher`**:

- `core/host/resolver.go` (Create) — `Resolver` deciding local-vs-remote per category from config; returns `contracts.Memory`.
- `bridge.go` (Modify `buildMemory`, lines 63–85) — route through the resolver instead of calling `p.Memory(...)` directly.
- `transportcfg.go` (Create) — reads which categories are remote (env `HERRSCHER_REMOTE` = comma list, e.g. `memory`); default empty = all-local.
- `serve.go` / supervisor (Modify) — when `memory` is remote, `serve` spawns a `herrscher plugin-host --category memory` child and the resolver waits for its announcement.
- `pluginhost.go` (Create) — the `plugin-host` subcommand: build the real plugin from `contracts.Default`, start a gRPC server, announce over NATS, block.
- `go.mod` (Modify) — add `github.com/Herrscherd/herrscher-transport` + nats.go.

---

## Task 1: Scaffold the `herrscher-transport` module

**Files:**
- Create: `/home/shan/dev/herrscher-transport/go.mod`
- Create: `/home/shan/dev/herrscher-transport/doc.go`
- Modify: `/home/shan/dev/go.work`

- [ ] **Step 1: Create the module directory and go.mod**

```bash
mkdir -p /home/shan/dev/herrscher-transport
cd /home/shan/dev/herrscher-transport
cat > go.mod <<'EOF'
module github.com/Herrscherd/herrscher-transport

go 1.25.0

require (
	github.com/Herrscherd/herrscher-contracts v0.1.4
	github.com/nats-io/nats.go v1.37.0
	google.golang.org/grpc v1.67.0
	google.golang.org/protobuf v1.34.2
)
EOF
```

- [ ] **Step 2: Add a package doc file so the package compiles empty**

```go
// Package transport carries Herrscher contracts ports over a generic
// JSON-over-gRPC service, with NATS for discovery and async events.
package transport
```

Write to `doc.go`.

- [ ] **Step 3: Register the module in the workspace**

Edit `/home/shan/dev/go.work` — add `./herrscher-transport` to the `use (...)` block (keep the list sorted):

```
use (
	./dctl
	./herrscher
	./herrscher-claude-backend
	./herrscher-contracts
	./herrscher-discord-gateway
	./herrscher-obsidian-memory
	./herrscher-orchestrator
	./herrscher-transport
)
```

- [ ] **Step 4: Pull deps and verify it builds**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go mod tidy && rtk proxy go build ./...`
Expected: no errors; `go.sum` populated.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-transport && git init -q && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add -A && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "chore: scaffold herrscher-transport module"
```

---

## Task 2: JSON codec

**Files:**
- Create: `/home/shan/dev/herrscher-transport/codec.go`
- Test: `/home/shan/dev/herrscher-transport/codec_test.go`

- [ ] **Step 1: Write the failing test**

```go
package transport

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestCodecRoundTripNode(t *testing.T) {
	in := contracts.Node{Key: "sessions/x", Kind: contracts.KindSession, Title: "X",
		Body: "hi", Links: []contracts.Link{{To: "a", Rel: "depends-on"}},
		Meta: map[string]string{"k": "v"}}
	b, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out contracts.Node
	if err := Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Key != in.Key || out.Title != in.Title || len(out.Links) != 1 || out.Meta["k"] != "v" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestCodecRoundTripNode`
Expected: FAIL — `undefined: Marshal` / `undefined: Unmarshal`.

- [ ] **Step 3: Write minimal implementation**

```go
package transport

import "encoding/json"

// Marshal encodes a contracts value for the wire. JSON keeps contracts the
// sole source of truth — no per-type proto.
func Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// Unmarshal decodes a wire payload into a contracts value.
func Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
```

Write to `codec.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestCodecRoundTripNode`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add codec.go codec_test.go && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: JSON codec for the transport wire"
```

---

## Task 3: Generate the generic gRPC service

**Files:**
- Create: `/home/shan/dev/herrscher-transport/proto/transport.proto`
- Create (generated): `/home/shan/dev/herrscher-transport/proto/transport.pb.go`, `transport_grpc.pb.go`

- [ ] **Step 1: Write the proto**

```proto
syntax = "proto3";

package herrscher.transport.v1;

option go_package = "github.com/Herrscherd/herrscher-transport/proto;transportpb";

// Plugin is the single generic service for every contracts port. The method
// name and JSON-encoded args travel inside the envelope, so contracts stays
// the sole source of truth for types.
service Plugin {
  rpc Call(MethodEnvelope) returns (ResultEnvelope);
}

message MethodEnvelope {
  string port = 1;          // e.g. "memory"
  string method = 2;        // e.g. "Recall"
  bytes json_payload = 3;   // JSON-encoded argument tuple
}

message ResultEnvelope {
  bytes json_payload = 1;   // JSON-encoded result tuple
  string error = 2;         // non-empty => the call returned an error
}
```

Write to `proto/transport.proto`.

- [ ] **Step 2: Generate the Go stubs**

Run:
```bash
cd /home/shan/dev/herrscher-transport
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
PATH="$PATH:$(go env GOPATH)/bin" protoc --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/transport.proto
```
Expected: `proto/transport.pb.go` and `proto/transport_grpc.pb.go` created.
(If `protoc` is absent: `sudo pacman -S protobuf` on this Arch host.)

- [ ] **Step 3: Verify it builds**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go build ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add proto && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: generic Plugin gRPC service (Call envelope)"
```

---

## Task 4: Memory server skeleton (gRPC server → real object)

**Files:**
- Create: `/home/shan/dev/herrscher-transport/memory_skeleton.go`
- Test: `/home/shan/dev/herrscher-transport/memory_skeleton_test.go`

The skeleton implements the generated `PluginServer.Call`. It switches on
`method`, decodes the arg tuple, calls the real `contracts.Memory`, and encodes
the result tuple. Port is `"memory"`.

- [ ] **Step 1: Write the failing test (uses a fake Memory, calls Call directly)**

```go
package transport

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
)

type fakeMem struct {
	recorded contracts.Node
	recall   contracts.Subgraph
}

func (f *fakeMem) Recall(_ context.Context, key string, depth int) (contracts.Subgraph, error) {
	f.recall.Root = contracts.Node{Key: key}
	return f.recall, nil
}
func (f *fakeMem) Record(_ context.Context, n contracts.Node) error { f.recorded = n; return nil }
func (f *fakeMem) Search(_ context.Context, q contracts.Query) ([]contracts.Node, error) {
	return []contracts.Node{{Key: "hit"}}, nil
}
func (f *fakeMem) Links(_ context.Context, from, to, rel string) error { return nil }
func (f *fakeMem) Close() error                                        { return nil }

func TestMemorySkeletonRecord(t *testing.T) {
	fake := &fakeMem{}
	srv := &memoryServer{real: fake}
	args, _ := Marshal([]any{contracts.Node{Key: "sessions/x", Title: "X"}})
	res, err := srv.Call(context.Background(),
		&pb.MethodEnvelope{Port: "memory", Method: "Record", JsonPayload: args})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if fake.recorded.Key != "sessions/x" {
		t.Fatalf("Record not dispatched, got %+v", fake.recorded)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestMemorySkeletonRecord`
Expected: FAIL — `undefined: memoryServer`.

- [ ] **Step 3: Write the skeleton**

```go
package transport

import (
	"context"
	"fmt"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
	"google.golang.org/grpc"
)

const PortMemory = "memory"

type memoryServer struct {
	pb.UnimplementedPluginServer
	real contracts.Memory
}

// RegisterMemorySkeleton wires a real Memory object behind the generic service.
func RegisterMemorySkeleton(s *grpc.Server, real contracts.Memory) {
	pb.RegisterPluginServer(s, &memoryServer{real: real})
}

func fail(err error) (*pb.ResultEnvelope, error) {
	return &pb.ResultEnvelope{Error: err.Error()}, nil
}

func (m *memoryServer) Call(ctx context.Context, env *pb.MethodEnvelope) (*pb.ResultEnvelope, error) {
	if env.Port != PortMemory {
		return fail(fmt.Errorf("transport: unknown port %q", env.Port))
	}
	switch env.Method {
	case "Recall":
		var a struct {
			Key   string
			Depth int
		}
		var tuple []any = []any{&a.Key, &a.Depth}
		if err := Unmarshal(env.JsonPayload, &tuple); err != nil {
			return fail(err)
		}
		sub, err := m.real.Recall(ctx, a.Key, a.Depth)
		if err != nil {
			return fail(err)
		}
		out, err := Marshal([]any{sub})
		if err != nil {
			return fail(err)
		}
		return &pb.ResultEnvelope{JsonPayload: out}, nil
	case "Record":
		var n contracts.Node
		tuple := []any{&n}
		if err := Unmarshal(env.JsonPayload, &tuple); err != nil {
			return fail(err)
		}
		if err := m.real.Record(ctx, n); err != nil {
			return fail(err)
		}
		return &pb.ResultEnvelope{}, nil
	case "Search":
		var q contracts.Query
		tuple := []any{&q}
		if err := Unmarshal(env.JsonPayload, &tuple); err != nil {
			return fail(err)
		}
		nodes, err := m.real.Search(ctx, q)
		if err != nil {
			return fail(err)
		}
		out, err := Marshal([]any{nodes})
		if err != nil {
			return fail(err)
		}
		return &pb.ResultEnvelope{JsonPayload: out}, nil
	case "Links":
		var a struct{ From, To, Rel string }
		tuple := []any{&a.From, &a.To, &a.Rel}
		if err := Unmarshal(env.JsonPayload, &tuple); err != nil {
			return fail(err)
		}
		if err := m.real.Links(ctx, a.From, a.To, a.Rel); err != nil {
			return fail(err)
		}
		return &pb.ResultEnvelope{}, nil
	case "Close":
		if err := m.real.Close(); err != nil {
			return fail(err)
		}
		return &pb.ResultEnvelope{}, nil
	default:
		return fail(fmt.Errorf("transport: unknown method memory.%s", env.Method))
	}
}
```

Write to `memory_skeleton.go`.

> NOTE on the arg-tuple decode: the heterogeneous-tuple `[]any{&x, &y}` form is verified end-to-end against the proxy in Task 5. If `encoding/json` positional decode into a mixed pointer slice proves awkward, switch both sides to a named struct per method (e.g. `recallArgs{Key string; Depth int}`) — the codec and wire format are unchanged; only the in-file arg shape changes. Keep proxy (Task 5) and skeleton symmetric.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestMemorySkeletonRecord`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add memory_skeleton.go memory_skeleton_test.go && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: memory server skeleton over the Call service"
```

---

## Task 5: Memory client proxy + round-trip integration test

**Files:**
- Create: `/home/shan/dev/herrscher-transport/memory_proxy.go`
- Test: `/home/shan/dev/herrscher-transport/memory_proxy_test.go`

`MemoryProxy` implements `contracts.Memory` and holds a `pb.PluginClient`. Each
method marshals its args into a tuple, calls `Call`, then unmarshals the result
or returns the envelope error.

- [ ] **Step 1: Write the failing integration test (real bufconn gRPC, proxy ↔ skeleton)**

```go
package transport

import (
	"context"
	"net"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func dialSkeleton(t *testing.T, real contracts.Memory) pb.PluginClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	RegisterMemorySkeleton(s, real)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewPluginClient(conn)
}

func TestMemoryProxyRoundTrip(t *testing.T) {
	fake := &fakeMem{}
	proxy := &MemoryProxy{client: dialSkeleton(t, fake)}

	if err := proxy.Record(context.Background(), contracts.Node{Key: "sessions/x", Title: "X"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if fake.recorded.Key != "sessions/x" {
		t.Fatalf("Record not propagated: %+v", fake.recorded)
	}

	sub, err := proxy.Recall(context.Background(), "sessions/x", 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if sub.Root.Key != "sessions/x" {
		t.Fatalf("Recall round-trip wrong: %+v", sub.Root)
	}

	hits, err := proxy.Search(context.Background(), contracts.Query{Text: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Key != "hit" {
		t.Fatalf("Search round-trip wrong: %+v", hits)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestMemoryProxyRoundTrip`
Expected: FAIL — `undefined: MemoryProxy`.

- [ ] **Step 3: Write the proxy**

```go
package transport

import (
	"context"
	"errors"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
)

// MemoryProxy is a contracts.Memory backed by a remote plugin over gRPC.
type MemoryProxy struct {
	client pb.PluginClient
}

// NewMemoryProxy builds a proxy over an established Plugin client.
func NewMemoryProxy(c pb.PluginClient) *MemoryProxy { return &MemoryProxy{client: c} }

func (p *MemoryProxy) call(ctx context.Context, method string, args ...any) (*pb.ResultEnvelope, error) {
	payload, err := Marshal(args)
	if err != nil {
		return nil, err
	}
	res, err := p.client.Call(ctx, &pb.MethodEnvelope{Port: PortMemory, Method: method, JsonPayload: payload})
	if err != nil {
		return nil, err // transport-level failure (peer down) — clear, typed
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res, nil
}

func (p *MemoryProxy) Recall(ctx context.Context, key string, depth int) (contracts.Subgraph, error) {
	res, err := p.call(ctx, "Recall", key, depth)
	if err != nil {
		return contracts.Subgraph{}, err
	}
	var out contracts.Subgraph
	tuple := []any{&out}
	if err := Unmarshal(res.JsonPayload, &tuple); err != nil {
		return contracts.Subgraph{}, err
	}
	return out, nil
}

func (p *MemoryProxy) Record(ctx context.Context, n contracts.Node) error {
	_, err := p.call(ctx, "Record", n)
	return err
}

func (p *MemoryProxy) Search(ctx context.Context, q contracts.Query) ([]contracts.Node, error) {
	res, err := p.call(ctx, "Search", q)
	if err != nil {
		return nil, err
	}
	var out []contracts.Node
	tuple := []any{&out}
	if err := Unmarshal(res.JsonPayload, &tuple); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *MemoryProxy) Links(ctx context.Context, from, to, rel string) error {
	_, err := p.call(ctx, "Links", from, to, rel)
	return err
}

func (p *MemoryProxy) Close() error {
	_, err := p.call(context.Background(), "Close")
	return err
}
```

Write to `memory_proxy.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestMemoryProxyRoundTrip`
Expected: PASS. If the heterogeneous-tuple decode in the skeleton fails here, apply the per-method-struct fallback noted in Task 4 and re-run.

- [ ] **Step 5: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add memory_proxy.go memory_proxy_test.go && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: memory client proxy with proxy<->skeleton round-trip"
```

---

## Task 6: NATS announce protocol + remote registry

**Files:**
- Create: `/home/shan/dev/herrscher-transport/subjects.go`
- Create: `/home/shan/dev/herrscher-transport/announce.go`
- Create: `/home/shan/dev/herrscher-transport/remote_registry.go`
- Test: `/home/shan/dev/herrscher-transport/announce_test.go`

- [ ] **Step 1: Write subjects.go**

```go
package transport

import "fmt"

// SubjectAnnounce is where a plugin process publishes its Announcement at boot.
const SubjectAnnounce = "plugins.announce"

// SessionEvents is the per-session async event subject (used by Backend later).
func SessionEvents(session string) string {
	return fmt.Sprintf("session.%s.events", session)
}
```

- [ ] **Step 2: Write the failing test (embedded NATS, announce → watch)**

```go
package transport

import (
	"context"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func runNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestAnnounceWatch(t *testing.T) {
	nc := runNATS(t)
	got := make(chan Announcement, 1)
	if err := WatchAnnouncements(nc, func(a Announcement) { got <- a }); err != nil {
		t.Fatalf("watch: %v", err)
	}
	ann := Announcement{
		Manifest:   contracts.Manifest{Kind: "sqlite", Category: contracts.CategoryMemory},
		GrpcAddr:   "127.0.0.1:50111",
		InstanceID: "abc",
	}
	if err := Announce(nc, ann); err != nil {
		t.Fatalf("announce: %v", err)
	}
	select {
	case a := <-got:
		if a.Manifest.Kind != "sqlite" || a.GrpcAddr != "127.0.0.1:50111" {
			t.Fatalf("bad announcement: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no announcement received")
	}
	_ = context.Background()
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestAnnounceWatch`
Expected: FAIL — `undefined: Announcement` / `WatchAnnouncements` / `Announce`.

- [ ] **Step 4: Write announce.go**

```go
package transport

import (
	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/nats-io/nats.go"
)

// Announcement is what a plugin process publishes at boot: its manifest
// (verbatim contracts.Manifest), where to reach its gRPC server, and a
// per-process identity.
type Announcement struct {
	Manifest   contracts.Manifest
	GrpcAddr   string
	InstanceID string
}

// Announce publishes an Announcement on SubjectAnnounce.
func Announce(nc *nats.Conn, ann Announcement) error {
	b, err := Marshal(ann)
	if err != nil {
		return err
	}
	return nc.Publish(SubjectAnnounce, b)
}

// WatchAnnouncements invokes fn for every Announcement seen on SubjectAnnounce.
func WatchAnnouncements(nc *nats.Conn, fn func(Announcement)) error {
	_, err := nc.Subscribe(SubjectAnnounce, func(msg *nats.Msg) {
		var ann Announcement
		if err := Unmarshal(msg.Data, &ann); err != nil {
			return
		}
		fn(ann)
	})
	return err
}
```

- [ ] **Step 5: Write remote_registry.go**

```go
package transport

import (
	"context"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RemoteEntry is one announced remote plugin.
type RemoteEntry struct {
	Manifest   contracts.Manifest
	GrpcAddr   string
	InstanceID string
}

// RemoteRegistry accumulates announcements and offers the same query surface
// as contracts.Registry, keyed by category.
type RemoteRegistry struct {
	mu      sync.RWMutex
	entries map[string]RemoteEntry // InstanceID -> entry
}

func NewRemoteRegistry() *RemoteRegistry {
	return &RemoteRegistry{entries: map[string]RemoteEntry{}}
}

// Observe records (or replaces) an announced plugin.
func (r *RemoteRegistry) Observe(a Announcement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[a.InstanceID] = RemoteEntry{Manifest: a.Manifest, GrpcAddr: a.GrpcAddr, InstanceID: a.InstanceID}
}

func (r *RemoteRegistry) byCategory(c contracts.Category) []RemoteEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []RemoteEntry
	for _, e := range r.entries {
		if e.Manifest.Category == c {
			out = append(out, e)
		}
	}
	return out
}

// Memories returns the announced memory plugins.
func (r *RemoteRegistry) Memories() []RemoteEntry { return r.byCategory(contracts.CategoryMemory) }

// DialMemory builds a contracts.Memory proxy over the entry's gRPC address.
func DialMemory(ctx context.Context, e RemoteEntry) (contracts.Memory, error) {
	conn, err := grpc.NewClient(e.GrpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	_ = ctx
	return NewMemoryProxy(pb.NewPluginClient(conn)), nil
}
```

- [ ] **Step 6: Run the announce test (and tidy for the new test dep)**

Run:
```bash
cd /home/shan/dev/herrscher-transport && rtk proxy go get github.com/nats-io/nats-server/v2@v2.10.22 && \
rtk proxy go mod tidy && rtk proxy go test ./... -run TestAnnounceWatch
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add subjects.go announce.go remote_registry.go announce_test.go go.mod go.sum && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: NATS announce protocol + remote registry + DialMemory"
```

---

## Task 7: Host resolver indirection (all-local default)

**Files:**
- Create: `/home/shan/dev/herrscher/transportcfg.go`
- Create: `/home/shan/dev/herrscher/core/host/resolver.go`
- Test: `/home/shan/dev/herrscher/core/host/resolver_test.go`
- Modify: `/home/shan/dev/herrscher/go.mod`

The resolver decides per category whether to resolve local or remote. This task
introduces it with **only the local branch live** (default), so behaviour is
bit-for-bit identical to today. The remote branch is wired in Task 8.

- [ ] **Step 1: Add the transport dep to the umbrella**

Run: `cd /home/shan/dev/herrscher && rtk proxy go get github.com/Herrscherd/herrscher-transport && rtk proxy go mod tidy`
Expected: `herrscher-transport` in `go.mod` (resolved via go.work locally).

- [ ] **Step 2: Write transportcfg.go**

```go
package main

import (
	"os"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// remoteCategories reads HERRSCHER_REMOTE (comma-separated category names) into
// a set. Empty/unset => all-local, today's behaviour.
func remoteCategories() map[contracts.Category]bool {
	out := map[contracts.Category]bool{}
	for _, c := range strings.Split(os.Getenv("HERRSCHER_REMOTE"), ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			out[contracts.Category(c)] = true
		}
	}
	return out
}
```

- [ ] **Step 3: Write the failing resolver test**

```go
package host

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type stubMem struct{ contracts.Memory }

func TestResolverLocalReturnsFactoryObject(t *testing.T) {
	want := &stubMem{}
	r := NewResolver(nil, "") // nil remote set => everything local
	got, err := r.Memory(context.Background(),
		[]contracts.Plugin{{
			Manifest: contracts.Manifest{Kind: "obsidian", Category: contracts.CategoryMemory},
			Memory:   func(context.Context, contracts.PluginConfig) (contracts.Memory, error) { return want, nil },
		}},
		func(string) string { return "" })
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != want {
		t.Fatalf("local resolve must return the factory object, got %v", got)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd /home/shan/dev/herrscher && rtk proxy go test ./core/host/ -run TestResolverLocalReturnsFactoryObject`
Expected: FAIL — `undefined: NewResolver`.

- [ ] **Step 5: Write resolver.go (local branch only; remote stubbed for Task 8)**

```go
package host

import (
	"context"
	"fmt"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// Resolver turns registered plugins into live port objects, choosing local
// (in-proc factory) or remote (gRPC proxy) per category. With a nil/empty
// remote set every category resolves local — today's behaviour.
type Resolver struct {
	remote  map[contracts.Category]bool
	NatsURL string // "" => nats.DefaultURL; consulted only on the remote path (Task 8)
}

func NewResolver(remote map[contracts.Category]bool, natsURL string) *Resolver {
	return &Resolver{remote: remote, NatsURL: natsURL}
}

func (r *Resolver) isRemote(c contracts.Category) bool {
	return r.remote != nil && r.remote[c]
}

// Memory resolves the first registered memory plugin. Local: call the factory.
// Remote: dialed in Task 8. Returns nil (no error) when none is registered —
// memory stays optional, matching buildMemory's contract.
func (r *Resolver) Memory(ctx context.Context, plugins []contracts.Plugin, getenv func(string) string) (contracts.Memory, error) {
	for _, p := range plugins {
		if p.Memory == nil {
			continue
		}
		if r.isRemote(contracts.CategoryMemory) {
			return r.dialRemoteMemory(ctx, p)
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, getenv)
		if err != nil {
			return nil, err
		}
		return p.Memory(ctx, cfg)
	}
	return nil, nil
}

// dialRemoteMemory is completed in Task 8.
func (r *Resolver) dialRemoteMemory(_ context.Context, p contracts.Plugin) (contracts.Memory, error) {
	return nil, fmt.Errorf("resolver: remote memory %q not wired yet", p.Manifest.Kind)
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /home/shan/dev/herrscher && rtk proxy go test ./core/host/ -run TestResolverLocalReturnsFactoryObject`
Expected: PASS.

- [ ] **Step 7: Route buildMemory through the resolver (still all-local)**

In `/home/shan/dev/herrscher/bridge.go`, replace the body of `buildMemory` (lines 63–85) so it delegates to the resolver while preserving the "disable on error, nil = off" behaviour:

```go
func buildMemory(ctx context.Context, verbose bool) contracts.Memory {
	disabled := func(kind string, err error) contracts.Memory {
		if verbose {
			fmt.Fprintf(os.Stderr, "herrscher bridge: memory %q disabled: %v\n", kind, err)
		}
		return nil
	}
	r := host.NewResolver(remoteCategories(), os.Getenv("HERRSCHER_NATS"))
	mem, err := r.Memory(ctx, contracts.Default.Memories(), os.Getenv)
	if err != nil {
		return disabled("memory", err)
	}
	return mem
}
```

Confirm `host` is already imported in `bridge.go`; if not, add `"github.com/Herrscherd/herrscher/core/host"` to its import block.

- [ ] **Step 8: Verify the umbrella still builds and behaves (all-local)**

Run: `cd /home/shan/dev/herrscher && rtk proxy go build ./... && rtk proxy go test ./...`
Expected: builds; existing tests pass (no behaviour change with `HERRSCHER_REMOTE` unset).

- [ ] **Step 9: Commit**

```bash
cd /home/shan/dev/herrscher && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add transportcfg.go core/host/resolver.go core/host/resolver_test.go bridge.go go.mod go.sum && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: host resolver indirection (all-local default)"
```

---

## Task 8: Wire memory-remote end to end (plugin-host child + supervisor + dial)

**Files:**
- Create: `/home/shan/dev/herrscher/pluginhost.go`
- Modify: `/home/shan/dev/herrscher/core/host/resolver.go` (`dialRemoteMemory`)
- Modify: `/home/shan/dev/herrscher/main.go` (register the `plugin-host` subcommand)
- Test: `/home/shan/dev/herrscher/core/host/resolver_remote_test.go`

`serve`, when a category is remote, spawns `herrscher plugin-host --category <c>`
as a supervised child. The child builds the real plugin from `contracts.Default`,
starts a gRPC server on an ephemeral port, and announces over NATS. The resolver
watches announcements, then dials a proxy.

> **Controller amendments (override the snippets below where they differ):**
>
> 1. **Heartbeat the announcement, don't announce once.** NATS core pub-sub has
>    no replay: a resolver that subscribes *after* a single boot announce never
>    sees it (this is exactly the ordering in the Step 5 test). The plugin-host
>    must re-announce on a `time.Ticker` (every 2s) until `ctx` is done — see the
>    corrected Step 1 below. This also gives presence/liveness for free.
> 2. **Spawn lives in `RunHub`, not `runServe`.** `runServe` wraps `ctx` in a
>    cancelable child only inside the TTY branch, so spawning there can't tie the
>    child to the daemon lifetime in both paths. Add a field to `host.Options`:
>    `RemoteCategories map[contracts.Category]bool`; set it in `runServe`
>    (`RemoteCategories: remoteCategories()`); spawn from `RunHub` (it already
>    holds the unified daemon `ctx` and `self`). See corrected Step 4.
> 3. **go.mod / the unpublished transport dep.** `herrscher-transport` is not yet
>    published, so `go get github.com/Herrscherd/herrscher-transport` will fail.
>    Do NOT run it. Add the require line manually with the workspace placeholder
>    `github.com/Herrscherd/herrscher-transport v0.0.0-00010101000000-000000000000`
>    (go.work resolves it locally; the tag is filled in at release, Task 10).
>    `go get` the nats deps normally (`github.com/nats-io/nats.go@v1.37.0` and
>    test-only `github.com/nats-io/nats-server/v2@v2.10.22`); they are published.
> 4. **Callback type is `transport.Announcement`** (not `contracts.Announcement`)
>    and `dialRemoteMemory` should `defer nc.Close()` once it has dialed.

- [ ] **Step 1: Write pluginhost.go (the child subcommand)**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

// runPluginHost serves one plugin category as a standalone process: build the
// real object from the registry, expose it over gRPC, announce on NATS, block.
func runPluginHost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin-host", flag.ContinueOnError)
	category := fs.String("category", "", "plugin category to host (e.g. memory)")
	natsURL := fs.String("nats", nats.DefaultURL, "NATS server URL")
	instanceID := fs.String("instance", "", "process instance id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *category != contracts.String(contracts.CategoryMemory) {
		return fmt.Errorf("plugin-host: unsupported category %q", *category)
	}

	var (
		real     contracts.Memory
		manifest contracts.Manifest
	)
	for _, p := range contracts.Default.Memories() {
		if p.Memory == nil {
			continue
		}
		cfg, err := contracts.Resolve(p.Manifest.Config, os.Getenv)
		if err != nil {
			return err
		}
		if real, err = p.Memory(ctx, cfg); err != nil {
			return err
		}
		manifest = p.Manifest
		break
	}
	if real == nil {
		return fmt.Errorf("plugin-host: no memory plugin registered")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	transport.RegisterMemorySkeleton(s, real)

	nc, err := nats.Connect(*natsURL)
	if err != nil {
		return err
	}
	defer nc.Close()
	if err := transport.Announce(nc, transport.Announcement{
		Manifest:   manifest,
		GrpcAddr:   lis.Addr().String(),
		InstanceID: *instanceID,
	}); err != nil {
		return err
	}

	go func() { <-ctx.Done(); s.GracefulStop() }()
	return s.Serve(lis)
}
```

> NOTE: `contracts.String(...)` is shorthand here for comparing the flag string to the category constant; if contracts has no such helper, compare with `*category != string(contracts.CategoryMemory)`. Use the plain `string(...)` conversion.

- [ ] **Step 2: Register the subcommand in main.go**

In `/home/shan/dev/herrscher/main.go`, find the command dispatch (the switch / map over `os.Args[1]`) and add a `"plugin-host"` case calling `runPluginHost(ctx, os.Args[2:])`. Match the existing dispatch style (e.g. alongside the `"bridge"` and `"serve"` cases).

- [ ] **Step 3: Implement dialRemoteMemory + an announcement-driven resolver path**

Replace the stub in `core/host/resolver.go` and add a NATS-backed wait. The
resolver, in remote mode, connects to NATS, watches announcements, and dials the
first memory entry that appears within a timeout:

```go
import (
	"errors"
	"time"

	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/nats-io/nats.go"
)

// NATSURL is where the resolver reaches NATS in remote mode ("" => default).
func (r *Resolver) natsURL() string {
	if r.NatsURL != "" {
		return r.NatsURL
	}
	return nats.DefaultURL
}

func (r *Resolver) dialRemoteMemory(ctx context.Context, _ contracts.Plugin) (contracts.Memory, error) {
	nc, err := nats.Connect(r.natsURL())
	if err != nil {
		return nil, err
	}
	reg := transport.NewRemoteRegistry()
	seen := make(chan struct{}, 1)
	if err := transport.WatchAnnouncements(nc, func(a contracts.Announcement) {
		reg.Observe(a)
		select {
		case seen <- struct{}{}:
		default:
		}
	}); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if mems := reg.Memories(); len(mems) > 0 {
			return transport.DialMemory(ctx, mems[0])
		}
		select {
		case <-seen:
		case <-deadline.C:
			return nil, errors.New("resolver: no remote memory announced within 10s")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
```

The `NatsURL` field and the 2-arg `NewResolver` signature already exist from Task 7; the `bridge.go` call site already passes `os.Getenv("HERRSCHER_NATS")`. This step only fills in `natsURL()` and `dialRemoteMemory` — no signature change.

> NOTE: `transport.WatchAnnouncements` takes `func(transport.Announcement)`, not `func(contracts.Announcement)`. Use `transport.Announcement` in the callback signature above. (Correct this when typing it in — the `contracts.` qualifier in the snippet is a typo.)

- [ ] **Step 4: Spawn the plugin-host child from the supervisor when memory is remote**

In `serve`'s startup (where the supervisor is created — `core/host/serve.go` around line 159–179, and the serve entrypoint in `/home/shan/dev/herrscher/serve.go`), when `remoteCategories()` contains `memory`, start a long-lived child before building sessions:

```go
// in serve startup, guarded by remoteCategories()[contracts.CategoryMemory]
self, _ := os.Executable()
go func() {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, self, "plugin-host", "--category", "memory",
			"--instance", "memory-0")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.Env = os.Environ()
		_ = cmd.Run()
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "serve: memory plugin-host exited, restarting in 3s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}()
```

This mirrors `supervisor.runLoop` (restart-on-exit). Place it where `serve` has the `ctx` and the self-binary path; reuse the existing `supervisor` package helper if cleaner.

- [ ] **Step 5: Write the remote integration test (embedded NATS + real child object in-proc)**

This test drives the resolver's remote path without spawning a process: it
starts a real gRPC skeleton + embedded NATS, announces, then asserts the
resolver dials and the proxy works.

```go
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
	// embedded NATS
	srv, _ := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1})
	go srv.Start()
	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)

	// a real gRPC memory skeleton over a fake object
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	gs := grpc.NewServer()
	transport.RegisterMemorySkeleton(gs, &recordingMem{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nc, _ := nats.Connect(srv.ClientURL())
	t.Cleanup(nc.Close)
	_ = transport.Announce(nc, transport.Announcement{
		Manifest:   contracts.Manifest{Kind: "sqlite", Category: contracts.CategoryMemory},
		GrpcAddr:   lis.Addr().String(),
		InstanceID: "memory-0",
	})

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
```

> NOTE: `recordingMem` embeds `contracts.Memory` so only the methods exercised need real bodies; the skeleton only calls `Record`/`Close` in this test. If the skeleton's `Record` path needs more, add the methods.

- [ ] **Step 6: Run the remote test**

Run: `cd /home/shan/dev/herrscher && rtk proxy go mod tidy && rtk proxy go test ./core/host/ -run TestResolverRemoteMemoryDials`
Expected: PASS.

- [ ] **Step 7: Verify all-local is still the default and untouched**

Run: `cd /home/shan/dev/herrscher && rtk proxy go build ./... && rtk proxy go test ./...`
Expected: builds; full suite passes with `HERRSCHER_REMOTE` unset.

- [ ] **Step 8: Manual smoke (optional but recommended)**

```bash
# terminal A: a local NATS (or rely on the embedded one only in tests)
# terminal B:
cd /home/shan/dev/herrscher && HERRSCHER_REMOTE=memory OBSIDIAN_VAULT=/tmp/vault \
  rtk proxy go run . plugin-host --category memory
# terminal C: confirm it announced and a serve run resolves memory remotely
```
Expected: `plugin-host` logs a gRPC listen addr and stays up; `serve` with `HERRSCHER_REMOTE=memory` reaches it.

- [ ] **Step 9: Commit**

```bash
cd /home/shan/dev/herrscher && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add pluginhost.go main.go core/host/resolver.go core/host/resolver_remote_test.go serve.go core/host/serve.go bridge.go go.mod go.sum && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "feat: wire memory-remote end to end (plugin-host child + resolver dial)"
```

---

## Task 9: Failure handling — proxy survives a child restart

**Files:**
- Modify: `/home/shan/dev/herrscher-transport/memory_proxy.go` (already returns transport errors; add a doc + a reconnect note)
- Test: `/home/shan/dev/herrscher-transport/memory_proxy_failure_test.go`

The proxy already returns a clear error on a transport-level failure (peer down).
gRPC's client reconnects automatically when the server comes back at the **same**
address; the supervisor restart + re-announce flow (re-dial on a new address) is
covered by the resolver re-running. This task locks the proxy's failure contract
with a test.

- [ ] **Step 1: Write the failing test (server stops mid-life, call returns error not panic)**

```go
package transport

import (
	"context"
	"net"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	pb "github.com/Herrscherd/herrscher-transport/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestProxyErrorsWhenPeerDown(t *testing.T) {
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	s := grpc.NewServer()
	RegisterMemorySkeleton(s, &fakeMem{})
	go func() { _ = s.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	proxy := NewMemoryProxy(pb.NewPluginClient(conn))

	// kill the peer, then a call must return an error (not hang forever, not panic)
	s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 1e9) // 1s
	defer cancel()
	if err := proxy.Record(ctx, contracts.Node{Key: "k"}); err == nil {
		t.Fatal("expected error when peer is down, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go test ./... -run TestProxyErrorsWhenPeerDown`
Expected: PASS (the proxy already surfaces transport errors). If it hangs, add a per-call default timeout in `call()` via `context.WithTimeout` when the passed ctx has no deadline, then re-run.

- [ ] **Step 3: If a timeout guard was needed, add it to call()**

```go
func (p *MemoryProxy) call(ctx context.Context, method string, args ...any) (*pb.ResultEnvelope, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	// ... unchanged ...
}
```

(Add `"time"` to imports.) Re-run Step 2; expect PASS.

- [ ] **Step 4: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add memory_proxy.go memory_proxy_failure_test.go && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "test: proxy returns a clear error when the remote peer is down"
```

---

## Task 10: README + publish the transport module

**Files:**
- Create: `/home/shan/dev/herrscher-transport/README.md`

- [ ] **Step 1: Write the README**

Document: what the module is (generic JSON-over-gRPC transport for contracts ports + NATS discovery), the `Call` envelope, the proxy/skeleton pair, the announce protocol, that contracts is the sole source of truth (no per-type proto), and the localhost-trust security posture. Mirror the tone of the other family READMEs (link to contracts + umbrella). Keep comments minimal per project style.

- [ ] **Step 2: Verify the whole module once more**

Run: `cd /home/shan/dev/herrscher-transport && rtk proxy go build ./... && rtk proxy go vet ./... && rtk proxy go test ./...`
Expected: all green.

- [ ] **Step 3: Verify the umbrella against the published tag (no go.work)**

This is the GOWORK=off gate: after the repo is pushed and tagged, the umbrella must build against the published transport tag, not the local checkout.

```bash
cd /home/shan/dev/herrscher && GOWORK=off rtk proxy go build ./...
```
Expected: builds once `herrscher-transport` is tagged and `go.mod` points at the tag. (Tagging + GitHub repo creation under the Herrscherd org is done at release time, same flow as the other plugins.)

- [ ] **Step 4: Commit**

```bash
cd /home/shan/dev/herrscher-transport && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com add README.md && \
git -c user.name=Akayashuu -c user.email=sauvageleo1@gmail.com commit -q -m "docs: herrscher-transport README"
```

---

## Out of scope for this plan (follow-on)

- **Migration steps 4–5:** backend (events over `session.<name>.events` per the spec), gateway, orchestrator categories; then the multi-machine config flip (mTLS + NATS creds). Each is its own plan; the resolver + transport module built here are the foundation they extend.
- **Backend event streaming:** `Backend.Respond`'s `onEvent` fan-out over NATS pub-sub is deliberately not built here — memory is unary-only, which is why it's the first bench.
- **GitHub repo creation + release tagging** for `herrscher-transport` under the Herrscherd org follows the same flow used for the other plugins (create public repo, push, tag, bump umbrella go.mod off the tag).
