package host

import (
	"context"
	"testing"

	"github.com/Herrscherd/herrscher/core/cli"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
)

// Dispatch routes a neutral argv through the real command registry the daemon
// builds, proving the seam a gateway uses is wired to the manager commands.
func TestHubDispatchSetSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := state.NewState(t.TempDir() + "/s.json")
	sup := supervisor.NewSupervisor(ctx, "/nonexistent/herrscher")
	o := Options{StatePath: t.TempDir() + "/s.json", DefaultCmd: "claude"}
	reg, err := buildRegistry(ctx, Deps{}, o, st, sup, "")
	if err != nil {
		t.Fatal(err)
	}
	h := newHub(ctx, st, sup, nil, t.TempDir(), reg, nil)

	if _, err := h.Dispatch(ctx, []string{"set", "source", "--path", "/src/x"}); err != nil {
		t.Fatal(err)
	}
	if st.SourceDir() != "/src/x" {
		t.Fatalf("dispatch did not persist source: %q", st.SourceDir())
	}
}

func newTestHub(t *testing.T) *hub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := state.NewState(t.TempDir() + "/s.json")
	sup := supervisor.NewSupervisor(ctx, "/nonexistent/herrscher") // never Start()ed in these tests
	return newHub(ctx, st, sup, nil, t.TempDir(), &cli.Registry{}, nil)
}

// Sessions mirrors persisted state as neutral SessionInfo for gateways.
func TestHubSessionsSnapshot(t *testing.T) {
	h := newTestHub(t)
	if got := h.Sessions(); len(got) != 0 {
		t.Fatalf("empty hub should report no sessions, got %+v", got)
	}
	if err := h.st.AddSession(state.Session{Name: "demo", ChannelID: "c1", Type: "text", Gateways: []string{"discord"}}); err != nil {
		t.Fatal(err)
	}
	got := h.Sessions()
	if len(got) != 1 || got[0].Name != "demo" || got[0].ChannelID != "c1" || got[0].Type != "text" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if len(got[0].Gateways) != 1 || got[0].Gateways[0] != "discord" {
		t.Fatalf("gateways not surfaced: %+v", got[0].Gateways)
	}
}

// reconcile brings persisted sessions live and tears down ones that vanished
// from state, so a gateway-driven create/close takes effect immediately.
func TestHubReconcileGoLiveGoDead(t *testing.T) {
	h := newTestHub(t)
	if err := h.st.AddSession(state.Session{Name: "live1", ChannelID: "c1", Type: "text"}); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	h.mu.Lock()
	_, up := h.live["live1"]
	h.mu.Unlock()
	if !up {
		t.Fatal("reconcile did not bring the persisted session live")
	}

	// Idempotent: a second reconcile must not double-register.
	h.reconcile()
	h.mu.Lock()
	n := len(h.live)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("reconcile not idempotent; live=%d", n)
	}

	if err := h.st.RemoveSession("live1"); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	h.mu.Lock()
	_, stillUp := h.live["live1"]
	h.mu.Unlock()
	if stillUp {
		t.Fatal("reconcile did not tear down the removed session")
	}
}
