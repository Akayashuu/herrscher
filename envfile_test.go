package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dctl.env")
	content := "# a comment\n" +
		"\n" +
		"DCTL_TEST_TOKEN=abc.def-ghi\n" +
		"export DCTL_TEST_EXPORTED=fromfile\n" +
		`DCTL_TEST_QUOTED="a b c"` + "\n" +
		"DCTL_TEST_EQUALS=k=v=w\n" +
		"   DCTL_TEST_SPACED   =  trimmed  \n" +
		"#DCTL_TEST_COMMENTED=nope\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"DCTL_TEST_TOKEN", "DCTL_TEST_EXPORTED", "DCTL_TEST_QUOTED",
		"DCTL_TEST_EQUALS", "DCTL_TEST_SPACED", "DCTL_TEST_COMMENTED",
	} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"DCTL_TEST_TOKEN":     "abc.def-ghi",
		"DCTL_TEST_EXPORTED":  "fromfile", // leading `export ` stripped
		"DCTL_TEST_QUOTED":    "a b c",    // surrounding quotes stripped, space preserved
		"DCTL_TEST_EQUALS":    "k=v=w",    // only the first '=' splits
		"DCTL_TEST_SPACED":    "trimmed",  // key and value trimmed
		"DCTL_TEST_COMMENTED": "",         // commented line ignored
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEnvFileRespectsExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dctl.env")
	if err := os.WriteFile(path, []byte("DCTL_TEST_PRESET=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DCTL_TEST_PRESET", "fromenv")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("DCTL_TEST_PRESET"); got != "fromenv" {
		t.Errorf("real env should win over the file, got %q", got)
	}
}

func TestLoadEnvFileMissingIsOK(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Errorf("missing env file should be a no-op, got %v", err)
	}
}
