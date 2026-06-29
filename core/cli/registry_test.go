package cli_test

import (
	"context"
	"strings"
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

func build(t *testing.T, cmds ...contracts.Cmd) *cli.Registry {
	t.Helper()
	var r cli.Registry
	for _, c := range cmds {
		if err := r.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	return &r
}

func TestDispatchResolvesLongestPath(t *testing.T) {
	got := ""
	r := build(t,
		contracts.New("session", "list").Do(func(_ context.Context, _ contracts.Input) (string, error) {
			got = "list"
			return "", nil
		}),
		contracts.New("session", "create").Param("name", "", true).
			Do(func(_ context.Context, in contracts.Input) (string, error) {
				got = "create:" + in.Get("name")
				return "", nil
			}),
	)
	if _, err := r.Dispatch(context.Background(), []string{"session", "create", "--name", "x"}); err != nil {
		t.Fatal(err)
	}
	if got != "create:x" {
		t.Fatalf("got %q", got)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	r := build(t, leaf("session", "list"))
	if _, err := r.Dispatch(context.Background(), []string{"nope"}); err == nil {
		t.Fatal("unknown command must error")
	}
}

func TestDispatchMissingRequiredParam(t *testing.T) {
	r := build(t, contracts.New("set", "home").Param("channel", "", true).
		Do(func(context.Context, contracts.Input) (string, error) { return "", nil }))
	if _, err := r.Dispatch(context.Background(), []string{"set", "home"}); err == nil {
		t.Fatal("missing required param must error")
	}
}

func TestDispatchBoolFlagAndRest(t *testing.T) {
	var in contracts.Input
	r := build(t, contracts.New("session", "create").
		Param("name", "", true).Param("shared", "", false).
		Do(func(_ context.Context, got contracts.Input) (string, error) { in = got; return "", nil }))
	_, err := r.Dispatch(context.Background(), []string{"session", "create", "extra", "--name", "x", "--shared"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Get("name") != "x" || !in.Bool("shared") {
		t.Fatalf("args = %+v", in.Args)
	}
	if len(in.Rest) != 1 || in.Rest[0] != "extra" {
		t.Fatalf("rest = %v", in.Rest)
	}
}

func TestDispatchReturnsHandlerOutput(t *testing.T) {
	r := build(t, leaf("session", "list"))
	out, err := r.Dispatch(context.Background(), []string{"session", "list"})
	if err != nil || out != "ran list" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestHelpRendersSortedUsage(t *testing.T) {
	r := build(t,
		contracts.New("set", "home").Help("set home").Param("channel", "", true).
			Do(func(context.Context, contracts.Input) (string, error) { return "", nil }),
		contracts.New("session", "list").Help("list sessions").
			Do(func(context.Context, contracts.Input) (string, error) { return "", nil }),
	)
	h := r.Help()
	// sorted: "session list" sorts before "set home"
	if want := "  session list"; !strings.Contains(h, want) {
		t.Fatalf("help missing %q:\n%s", want, h)
	}
	if !strings.Contains(h, "--channel <channel>") {
		t.Fatalf("help missing required-param rendering:\n%s", h)
	}
}

func TestRunInvokesCommandByExactPathWithTypedInput(t *testing.T) {
	var gotName string
	r := build(t,
		contracts.New("session", "create").Param("name", "", true).
			Do(func(_ context.Context, in contracts.Input) (string, error) {
				gotName = in.Get("name")
				return "created", nil
			}),
	)
	out, err := r.Run(context.Background(), []string{"session", "create"},
		contracts.Input{Args: map[string]string{"name": "main"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "created" || gotName != "main" {
		t.Fatalf("Run did not pass typed input: out=%q name=%q", out, gotName)
	}
}

func TestRunRejectsUnknownPath(t *testing.T) {
	r := build(t, leaf("session", "list"))
	if _, err := r.Run(context.Background(), []string{"session", "nope"}, contracts.Input{}); err == nil {
		t.Fatal("Run must reject an unknown path")
	}
}

func TestRunChecksRequiredParams(t *testing.T) {
	r := build(t,
		contracts.New("session", "create").Param("name", "", true).
			Do(func(context.Context, contracts.Input) (string, error) { return "", nil }),
	)
	if _, err := r.Run(context.Background(), []string{"session", "create"}, contracts.Input{Args: map[string]string{}}); err == nil {
		t.Fatal("Run must reject missing required param")
	}
}
