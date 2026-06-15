package kernel

import "testing"

func TestConversationValue(t *testing.T) {
	a := Conversation{Gateway: "discord", ID: "123"}
	b := Conversation{Gateway: "discord", ID: "123"}
	if a != b {
		t.Fatalf("equal conversations should compare equal")
	}
	if (Conversation{Gateway: "discord", ID: "123"}) == (Conversation{Gateway: "telegram", ID: "123"}) {
		t.Fatalf("different gateways must not be equal")
	}
}

func TestChoiceFields(t *testing.T) {
	c := Choice{Label: "Yes", Value: "y"}
	if c.Label != "Yes" || c.Value != "y" {
		t.Fatalf("unexpected choice %+v", c)
	}
}
