package discord

import "testing"

func TestCommandsCatalogHasSession(t *testing.T) {
	cmds := Commands()
	var names []string
	for _, c := range cmds {
		names = append(names, c["name"].(string))
	}
	want := []string{"set", "session", "workspace", "service", "allow"}
	for _, n := range want {
		found := false
		for _, got := range names {
			if got == n {
				found = true
			}
		}
		if !found {
			t.Fatalf("command catalog missing %q (got %v)", n, names)
		}
	}
}
