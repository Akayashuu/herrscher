package main

import (
	"context"
	"strings"
	"testing"
)

// TestRunPluginHostFailsClosedOnHalfTLS proves the plugin-host refuses to start
// on a half-configured TLS env — every partial permutation — rather than
// silently serving plaintext. Validation runs before any plugin is built, so the
// error is the TLS one regardless of plugin state.
func TestRunPluginHostFailsClosedOnHalfTLS(t *testing.T) {
	cases := []struct {
		name          string
		ca, cert, key string
	}{
		{"key only", "", "", "/k.pem"},
		{"cert only", "", "/c.pem", ""},
		{"ca only", "/a.pem", "", ""},
		{"cert+key, no ca", "", "/c.pem", "/k.pem"},
		{"ca+key, no cert", "/a.pem", "", "/k.pem"},
		{"ca+cert, no key", "/a.pem", "/c.pem", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERRSCHER_TLS_CA", tc.ca)
			t.Setenv("HERRSCHER_TLS_CERT", tc.cert)
			t.Setenv("HERRSCHER_TLS_KEY", tc.key)
			err := runPluginHost(context.Background(), []string{"--category", "memory"})
			if err == nil || !strings.Contains(err.Error(), "TLS half-configured") {
				t.Fatalf("want a fail-closed TLS error, got %v", err)
			}
		})
	}
}
