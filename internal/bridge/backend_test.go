package bridge

import (
	"errors"
	"testing"
)

func TestResolveBackendDefaultsToStream(t *testing.T) {
	cases := []struct {
		backend string
		stream  bool
		want    string
	}{
		{"", true, "stream"},         // default: nothing set → stream
		{"", false, "oneshot"},       // legacy --stream=false → oneshot
		{"stream", true, "stream"},   // explicit wins over the default
		{"stream", false, "stream"},  // explicit wins over --stream
		{"oneshot", true, "oneshot"}, // explicit wins
		{"tmux", false, "tmux"},      // explicit wins
	}
	for _, c := range cases {
		if got := resolveBackend(c.backend, c.stream); got != c.want {
			t.Errorf("resolveBackend(%q, %v) = %q, want %q", c.backend, c.stream, got, c.want)
		}
	}
}

func TestAvailableBackendFallsBackWhenTmuxMissing(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("not found") }
	present := func(string) (string, error) { return "/usr/bin/tmux", nil }

	if got, fell := availableBackend("tmux", missing); got != "stream" || !fell {
		t.Errorf("tmux missing: got (%q, %v), want (\"stream\", true)", got, fell)
	}
	if got, fell := availableBackend("tmux", present); got != "tmux" || fell {
		t.Errorf("tmux present: got (%q, %v), want (\"tmux\", false)", got, fell)
	}
	// Non-tmux backends are never downgraded, even if the probe would fail.
	if got, fell := availableBackend("stream", missing); got != "stream" || fell {
		t.Errorf("stream: got (%q, %v), want (\"stream\", false)", got, fell)
	}
}
