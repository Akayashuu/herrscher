package host

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
)

// TestResolverRemoteMemoryOverMTLS is the C4 end-to-end: with a generated CA/cert
// pair the resolver negotiates mTLS to a remote memory category, and a client
// without valid credentials is rejected. The same dual-usage leaf serves as both
// the server and client identity (both signed by the test CA).
func TestResolverRemoteMemoryOverMTLS(t *testing.T) {
	dir := genTestCerts(t)
	tlsCfg := transport.TLSConfig{
		CAFile:   filepath.Join(dir, "ca.pem"),
		CertFile: filepath.Join(dir, "leaf.pem"),
		KeyFile:  filepath.Join(dir, "leaf-key.pem"),
	}
	serverCreds, err := tlsCfg.ServerCredentials()
	if err != nil {
		t.Fatalf("server creds: %v", err)
	}

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
	gs := grpc.NewServer(grpc.Creds(serverCreds))
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

	// With client credentials the mTLS handshake succeeds and the proxy call
	// reaches the remote memory.
	clientCreds, err := tlsCfg.ClientCredentials()
	if err != nil {
		t.Fatalf("client creds: %v", err)
	}
	r := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, srv.ClientURL())
	r.SetCredentials(clientCreds)
	mem, err := r.Memory(context.Background(), remotePlugin(), func(string) string { return "" })
	if err != nil {
		t.Fatalf("remote resolve over mTLS: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := mem.Record(ctx, contracts.Node{Key: "k"}); err != nil {
		t.Fatalf("Record over mTLS: %v", err)
	}

	// A plaintext client (no credentials) is rejected by the mTLS server: the
	// proxy dials lazily, so the rejection surfaces on the first call.
	plain := NewResolver(map[contracts.Category]bool{contracts.CategoryMemory: true}, srv.ClientURL())
	memPlain, err := plain.Memory(context.Background(), remotePlugin(), func(string) string { return "" })
	if err != nil {
		t.Fatalf("resolve (lazy dial) should not fail: %v", err)
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rcancel()
	if err := memPlain.Record(rctx, contracts.Node{Key: "k"}); err == nil {
		t.Fatal("a plaintext client must be rejected by the mTLS server")
	}
}

// genTestCerts writes a self-signed CA plus one leaf (signed by the CA) usable as
// both server and client identity (SAN 127.0.0.1, server+client ExtKeyUsage) into
// a temp dir, returning the dir.
func genTestCerts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "herrscher-test-ca"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(1<<31, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER)
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	writePEM(t, filepath.Join(dir, "leaf.pem"), "CERTIFICATE", leafDER)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, filepath.Join(dir, "leaf-key.pem"), "EC PRIVATE KEY", keyDER)
	return dir
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}
