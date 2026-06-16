package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSessionMaxIdleDays(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"sessionMaxIdleDays": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.SessionMaxIdleDays != 7 {
		t.Fatalf("SessionMaxIdleDays = %d, want 7", c.SessionMaxIdleDays)
	}
}
