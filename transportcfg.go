package main

import (
	"log/slog"
	"os"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	transport "github.com/Herrscherd/herrscher-transport"
	"github.com/Herrscherd/herrscher/core/host"
	"google.golang.org/grpc/credentials"
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

// tlsConfigFromEnv reads the mTLS file paths the multi-machine transport uses.
// All three empty => plaintext loopback (the single-host default).
func tlsConfigFromEnv() transport.TLSConfig {
	return transport.TLSConfig{
		CAFile:   os.Getenv("HERRSCHER_TLS_CA"),
		CertFile: os.Getenv("HERRSCHER_TLS_CERT"),
		KeyFile:  os.Getenv("HERRSCHER_TLS_KEY"),
	}
}

// bindAddr is the plugin-host listener address. The default stays loopback so
// single-host deployments are byte-for-byte unchanged; HERRSCHER_BIND opens the
// listener to other hosts (pair it with TLS, or the transport serves plaintext
// off-loopback).
func bindAddr() string {
	if a := strings.TrimSpace(os.Getenv("HERRSCHER_BIND")); a != "" {
		return a
	}
	return "127.0.0.1:0"
}

// remoteClientCredentials returns the mTLS credentials the resolver dials with,
// or nil for plaintext loopback. It fails closed: a half-configured TLS env is a
// startup error, never a silent fall back to plaintext.
func remoteClientCredentials() (credentials.TransportCredentials, error) {
	cfg := tlsConfigFromEnv()
	if !cfg.Enabled() {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg.ClientCredentials()
}

// newResolver builds the category resolver shared by buildMemory,
// buildOrchestrator and the backend factory: it wires the remote-category set,
// NATS URL, logger, and the mTLS dial credentials (fail-closed on half-config).
func newResolver(log *slog.Logger) (*host.Resolver, error) {
	r := host.NewResolver(remoteCategories(), os.Getenv("HERRSCHER_NATS"))
	r.SetLogger(log)
	creds, err := remoteClientCredentials()
	if err != nil {
		return nil, err
	}
	r.SetCredentials(creds)
	return r, nil
}
