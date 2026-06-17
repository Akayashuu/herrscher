package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// fakeGateway is a minimal Gateway used only to make a GatewaySet non-zero.
type fakeGateway struct{}

func (fakeGateway) Manifest() contracts.Manifest { return contracts.Manifest{} }
func (fakeGateway) Post(context.Context, contracts.Conversation, string) (contracts.MessageID, error) {
	return "", nil
}
func (fakeGateway) Reply(context.Context, contracts.Conversation, contracts.MessageID, string) (contracts.MessageID, error) {
	return "", nil
}
func (fakeGateway) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (fakeGateway) Menu(context.Context, contracts.Conversation, contracts.MessageID, string, []contracts.Choice) error {
	return nil
}

func gw(kind string, fail bool) contracts.Plugin {
	return contracts.Plugin{
		Manifest: contracts.Manifest{Kind: kind, Category: contracts.CategoryGateway},
		Gateway: func(context.Context, contracts.PluginConfig) (contracts.GatewaySet, error) {
			if fail {
				return contracts.GatewaySet{}, errors.New("boom")
			}
			return contracts.GatewaySet{Gateway: fakeGateway{}}, nil
		},
	}
}

func TestBuildHubAll(t *testing.T) {
	hub, err := BuildHub(context.Background(), []contracts.Plugin{gw("discord", false), gw("terminal", false)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.Kinds(); len(got) != 2 || got[0] != "discord" || got[1] != "terminal" {
		t.Fatalf("Kinds() = %v, want [discord terminal]", got)
	}
	if _, ok := hub.Get("terminal"); !ok {
		t.Error("terminal gateway should be present")
	}
}

func TestBuildHubToleratesFailure(t *testing.T) {
	hub, err := BuildHub(context.Background(), []contracts.Plugin{gw("discord", true), gw("terminal", false)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.Kinds(); len(got) != 1 || got[0] != "terminal" {
		t.Fatalf("Kinds() = %v, want [terminal]", got)
	}
}

func TestBuildHubAllFailedAggregates(t *testing.T) {
	_, err := BuildHub(context.Background(), []contracts.Plugin{gw("discord", true)}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "discord") {
		t.Fatalf("want aggregated error mentioning discord, got %v", err)
	}
}

func TestFirstReturnsRegistrationOrder(t *testing.T) {
	hub, _ := BuildHub(context.Background(), []contracts.Plugin{gw("discord", false), gw("terminal", false)}, func(string) string { return "" })
	set, ok := hub.First()
	if !ok {
		t.Fatal("First() should report a built set")
	}
	if set.Gateway == nil {
		t.Error("First() should return the first built set")
	}
}

func TestFirstEmptyHub(t *testing.T) {
	if _, ok := (&GatewayHub{}).First(); ok {
		t.Error("First() on an empty hub should report ok=false")
	}
}

func TestBuildHubRejectsNilGatewaySet(t *testing.T) {
	nilSet := contracts.Plugin{
		Manifest: contracts.Manifest{Kind: "empty", Category: contracts.CategoryGateway},
		Gateway: func(context.Context, contracts.PluginConfig) (contracts.GatewaySet, error) {
			return contracts.GatewaySet{}, nil
		},
	}
	if _, err := BuildHub(context.Background(), []contracts.Plugin{nilSet}, func(string) string { return "" }); err == nil {
		t.Fatal("a factory returning a nil Gateway should be skipped and, as the only plugin, fail the build")
	}
}
