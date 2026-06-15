package discord

import (
	"strings"
	"testing"
)

func TestDctlCommandsHasWorkspaceAndOptions(t *testing.T) {
	cmds := Commands()
	var names []string
	for _, c := range cmds {
		names = append(names, c["name"].(string))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "workspace") {
		t.Fatalf("expected a /workspace command, got %v", names)
	}

	// /set must have a workspace subcommand.
	set := findCmd(t, cmds, "set")
	if !hasSub(set, "workspace") {
		t.Fatalf("/set missing workspace subcommand")
	}

	// /session create must expose project + clone options.
	sess := findCmd(t, cmds, "session")
	create := findSub(t, sess, "create")
	if !hasOpt(create, "project") || !hasOpt(create, "clone") {
		t.Fatalf("/session create missing project/clone options")
	}
}

func findCmd(t *testing.T, cmds []map[string]any, name string) map[string]any {
	t.Helper()
	for _, c := range cmds {
		if c["name"] == name {
			return c
		}
	}
	t.Fatalf("command %q not found", name)
	return nil
}

func subs(cmd map[string]any) []map[string]any {
	raw, _ := cmd["options"].([]map[string]any)
	return raw
}

func hasSub(cmd map[string]any, name string) bool {
	for _, o := range subs(cmd) {
		if o["name"] == name {
			return true
		}
	}
	return false
}

func findSub(t *testing.T, cmd map[string]any, name string) map[string]any {
	t.Helper()
	for _, o := range subs(cmd) {
		if o["name"] == name {
			return o
		}
	}
	t.Fatalf("subcommand %q not found", name)
	return nil
}

func hasOpt(sub map[string]any, name string) bool {
	opts, _ := sub["options"].([]map[string]any)
	for _, o := range opts {
		if o["name"] == name {
			return true
		}
	}
	return false
}
