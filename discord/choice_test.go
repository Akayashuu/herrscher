package discord

import "testing"

func TestChoiceCustomIDRoundTrip(t *testing.T) {
	id := ChoiceCustomID("mysession")
	name, ok := ParseChoiceCustomID(id)
	if !ok || name != "mysession" {
		t.Fatalf("round trip failed: id=%q name=%q ok=%v", id, name, ok)
	}
	if _, ok := ParseChoiceCustomID("not-a-choice"); ok {
		t.Fatalf("non-choice custom_id must report ok=false")
	}
}
