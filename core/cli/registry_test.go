package cli_test

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/cli"
)

func leaf(path ...string) contracts.Cmd {
	return contracts.New(path...).Do(func(context.Context, contracts.Input) (string, error) {
		return "ran " + path[len(path)-1], nil
	})
}

func TestAddRejectsDuplicatePath(t *testing.T) {
	var r cli.Registry
	if err := r.Add(leaf("set", "home")); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(leaf("set", "home")); err == nil {
		t.Fatal("duplicate path must be rejected")
	}
}

func TestAddRejectsEmptyPath(t *testing.T) {
	var r cli.Registry
	if err := r.Add(contracts.New().Do(nil)); err == nil {
		t.Fatal("empty path must be rejected")
	}
}
