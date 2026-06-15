package discord

import "testing"

func TestCloseNameOptionIsAutocomplete(t *testing.T) {
	// Walk the declarative command set to the /session close → name option.
	var session map[string]any
	for _, c := range Commands() {
		if c["name"] == "session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no /session command")
	}
	for _, sub := range session["options"].([]map[string]any) {
		if sub["name"] != "close" {
			continue
		}
		for _, opt := range sub["options"].([]map[string]any) {
			if opt["name"] == "name" {
				if opt["autocomplete"] != true {
					t.Fatalf("close→name must set autocomplete:true, got %v", opt["autocomplete"])
				}
				return
			}
		}
	}
	t.Fatal("did not find /session close → name option")
}
