package discord

import "testing"

// hasSessionSub walks the declarative command set for a (top, name) pair.
func hasSessionSub(top, name string) bool {
	for _, c := range Commands() {
		if c["name"] != top {
			continue
		}
		for _, o := range c["options"].([]map[string]any) {
			if o["name"] == name {
				return true
			}
		}
	}
	return false
}

func TestSessionAllowGroupDeclared(t *testing.T) {
	if !hasSessionSub("session", "allow") {
		t.Fatal("session command must declare an 'allow' sub-command group")
	}
	if !hasSessionSub("session", "who") {
		t.Fatal("session command must declare a 'who' subcommand")
	}
}
