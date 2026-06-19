package control

import (
	"path/filepath"
	"strings"
	"testing"
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
