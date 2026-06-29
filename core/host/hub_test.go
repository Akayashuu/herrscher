package host

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/cli"
	"github.com/Herrscherd/herrscher/core/internal/state"
	"github.com/Herrscherd/herrscher/core/internal/supervisor"
)

// hubWith builds a hub over a registry holding one recording command at path, so
// a test can assert how the hub's typed methods map a spec into a typed Input
// without standing up the whole session machinery.
func hubWith(t *testing.T, path []string, got *contracts.Input) *hub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := state.NewState(t.TempDir() + "/s.json")
	sup := supervisor.NewSupervisor(ctx, "/nonexistent/herrscher")
	var r cli.Registry
	cmd := contracts.New(path...).Param("name", "", true).
		Do(func(_ context.Context, in contracts.Input) (string, error) {
			*got = in
			return "ok", nil
		})
	if err := r.Add(cmd); err != nil {
		t.Fatal(err)
	}
	return newHub(ctx, st, sup, nil, t.TempDir(), &r, nil)
}

// Create maps the typed CreateSession into the flags the session-create command
// declares, so a renamed field is a compile error, not a silent argv typo.
func TestHubCreateMapsSpecToTypedInput(t *testing.T) {
	var got contracts.Input
	h := hubWith(t, []string{"session", "create"}, &got)
	if _, err := h.Create(context.Background(), contracts.CreateSession{
		Name: "main", Project: "alpha", Gateways: []string{"discord", "terminal"},
		TerminalOnly: true, Shared: true, Agent: "bishop", ConsolidateEvery: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if got.Get("name") != "main" || got.Get("project") != "alpha" || got.Get("agent") != "bishop" {
		t.Fatalf("string fields not mapped: %+v", got.Args)
	}
	if got.Get("gateways") != "discord,terminal" {
		t.Fatalf("gateways not joined: %q", got.Get("gateways"))
	}
	if !got.Bool("terminal_only") || !got.Bool("shared") {
		t.Fatalf("bool flags not mapped: %+v", got.Args)
	}
	if got.Get("consolidate_every") != "3" {
		t.Fatalf("consolidate_every not mapped: %q", got.Get("consolidate_every"))
	}
}

// An omitted optional field must not appear in the Input, so it stays "flag
// unset" rather than an empty-string value the command might misread.
func TestHubCreateOmitsUnsetFields(t *testing.T) {
	var got contracts.Input
	h := hubWith(t, []string{"session", "create"}, &got)
	if _, err := h.Create(context.Background(), contracts.CreateSession{Name: "main"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"project", "gateways", "agent", "shared", "terminal_only", "consolidate_every"} {
		if _, ok := got.Args[k]; ok {
			t.Fatalf("unset field %q must be absent, got %q", k, got.Args[k])
		}
	}
}

func TestHubCloseMapsNameAndForce(t *testing.T) {
	var got contracts.Input
	h := hubWith(t, []string{"session", "close"}, &got)
	if _, err := h.Close(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	if got.Get("name") != "demo" || !got.Bool("force") {
		t.Fatalf("close did not map name/force: %+v", got.Args)
	}
}

func TestHubCloseOmitsForceWhenFalse(t *testing.T) {
	var got contracts.Input
	h := hubWith(t, []string{"session", "close"}, &got)
	if _, err := h.Close(context.Background(), "demo", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Args["force"]; ok {
		t.Fatalf("force must be absent when false: %+v", got.Args)
	}
}

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
