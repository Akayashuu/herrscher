package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// loadEnvFile reads KEY=VALUE lines from path into the process environment so
// the daemon's secrets (DISCORD_BOT_TOKEN, DCTL_OWNER_ID, …) need not be sourced
// by a shell wrapper — every platform's service can just exec `dctl serve
// --env-file PATH`. Blank lines and `#` comments are skipped, an optional
// leading `export ` is ignored, surrounding quotes on the value are stripped,
// and only the first `=` splits key from value (values may contain `=`). Keys
// already present in the environment are left untouched (real env wins). A
// missing file is not an error — the service may be installed before its
// secrets are filled in.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue // not a KEY=VALUE line
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue // real environment takes precedence over the file
		}
		if err := os.Setenv(key, unquoteEnv(strings.TrimSpace(val))); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return sc.Err()
}

// unquoteEnv strips a single matching pair of surrounding single or double
// quotes, so `KEY="a b"` and `KEY='a b'` yield `a b`.
func unquoteEnv(s string) string {
	if len(s) >= 2 {
		if c := s[0]; (c == '"' || c == '\'') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
