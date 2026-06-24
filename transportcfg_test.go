package main

import "testing"

func TestBindAddrDefaultsToLoopback(t *testing.T) {
	t.Setenv("HERRSCHER_BIND", "")
	if got := bindAddr(); got != "127.0.0.1:0" {
		t.Fatalf("default bind = %q, want 127.0.0.1:0", got)
	}
	t.Setenv("HERRSCHER_BIND", "0.0.0.0:9443")
	if got := bindAddr(); got != "0.0.0.0:9443" {
		t.Fatalf("override bind = %q, want 0.0.0.0:9443", got)
	}
}

func TestTLSConfigFromEnvReadsPaths(t *testing.T) {
	t.Setenv("HERRSCHER_TLS_CA", "/ca.pem")
	t.Setenv("HERRSCHER_TLS_CERT", "/cert.pem")
	t.Setenv("HERRSCHER_TLS_KEY", "/key.pem")
	c := tlsConfigFromEnv()
	if c.CAFile != "/ca.pem" || c.CertFile != "/cert.pem" || c.KeyFile != "/key.pem" {
		t.Fatalf("tlsConfigFromEnv = %+v", c)
	}
}

func TestRemoteClientCredentialsFailsClosedOnHalfConfig(t *testing.T) {
	// Only the key set: a half-configuration must error, never fall back to
	// plaintext (the off-loopback safety guarantee).
	t.Setenv("HERRSCHER_TLS_CA", "")
	t.Setenv("HERRSCHER_TLS_CERT", "")
	t.Setenv("HERRSCHER_TLS_KEY", "/key.pem")
	if _, err := remoteClientCredentials(); err == nil {
		t.Fatal("half-configured TLS must fail closed, got nil error")
	}
}

func TestRemoteClientCredentialsNilWhenUnset(t *testing.T) {
	t.Setenv("HERRSCHER_TLS_CA", "")
	t.Setenv("HERRSCHER_TLS_CERT", "")
	t.Setenv("HERRSCHER_TLS_KEY", "")
	creds, err := remoteClientCredentials()
	if err != nil {
		t.Fatalf("plaintext must be valid: %v", err)
	}
	if creds != nil {
		t.Fatal("no TLS env => nil credentials (plaintext loopback)")
	}
}
