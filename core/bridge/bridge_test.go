package bridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// recGW records outbound port actions for assertions.
type recGW struct {
	replies []string
	posts   []string
	menus   []string
	reacts  []string
}

func (g *recGW) Manifest() contracts.Manifest {
	return contracts.Manifest{Capabilities: contracts.Capabilities{Reactions: true, SelectMenus: true, Replies: true}}
}

func (g *recGW) Post(_ context.Context, _ contracts.Conversation, text string) (contracts.MessageID, error) {
	g.posts = append(g.posts, text)
	return "", nil
}

func (g *recGW) Reply(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	g.replies = append(g.replies, text)
	return "", nil
}

func (g *recGW) React(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, emoji string) error {
	g.reacts = append(g.reacts, emoji)
	return nil
}

func (g *recGW) Menu(_ context.Context, _ contracts.Conversation, _ contracts.MessageID, prompt string, _ []contracts.Choice) error {
	g.menus = append(g.menus, prompt)
	return nil
}

func TestPostResultEmitsViaGateway(t *testing.T) {
	rec := &recGW{}
	conv := contracts.Conversation{Gateway: "discord", ID: "chan"}
	postResultGW(context.Background(), rec, conv, "mid", "hello world")
	if len(rec.replies) != 1 || rec.replies[0] != "hello world" {
		t.Fatalf("postResult should reply via the gateway: %+v", rec)
	}
}

func TestRecordParticipantAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "participants", "demo.log")
	recordParticipant(path, "u1")
	recordParticipant(path, "u1") // idempotent
	recordParticipant(path, "u2")
	got := state.ReadParticipants(path)
	if len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("expected [u1 u2], got %+v", got)
	}
}

func TestRecordParticipantEmptyPathNoop(t *testing.T) {
	// must not panic or create anything when no journal configured
	recordParticipant("", "u1")
}

func TestAuthorizedEnforcesAllowlist(t *testing.T) {
	sp := filepath.Join(t.TempDir(), "state.json")
	st := state.NewState(sp)
	if err := st.AddSession(state.Session{Name: "demo", ChannelID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSessionAllow("demo", "u1"); err != nil {
		t.Fatal(err)
	}
	st.AddAllow("admin") // global allowlist

	o := Options{AllowState: sp, Session: "demo"}
	if !authorized(o, "u1") {
		t.Fatal("u1 is on the per-session allowlist → must be authorized")
	}
	if !authorized(o, "admin") {
		t.Fatal("globally-allowed admin → must be authorized (global OR per-session)")
	}
	if authorized(o, "stranger") {
		t.Fatal("stranger is on no list → must be rejected")
	}
}

func TestAuthorizerReloadsOnStateChange(t *testing.T) {
	// The cache must not defeat live /session allow changes: a write that adds
	// a user changes the file's mtime/size, so the next check reloads.
	sp := filepath.Join(t.TempDir(), "state.json")
	st := state.NewState(sp)
	if err := st.AddSession(state.Session{Name: "demo", ChannelID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSessionAllow("demo", "u1"); err != nil {
		t.Fatal(err)
	}
	a := &authorizer{o: Options{AllowState: sp, Session: "demo"}}
	if !a.allowed("u1") || a.allowed("u2") {
		t.Fatal("initial: u1 allowed, u2 denied")
	}
	if _, err := st.AddSessionAllow("demo", "u2"); err != nil {
		t.Fatal(err)
	}
	if !a.allowed("u2") {
		t.Fatal("cached authorizer must pick up the newly-allowed u2")
	}
}

func TestAuthorizedNoEnforcementWhenStateEmpty(t *testing.T) {
	// Standalone bridge (no --allow-state) answers everyone, preserving old behaviour.
	if !authorized(Options{AllowState: ""}, "anyone") {
		t.Fatal("empty AllowState must disable enforcement (answer everyone)")
	}
}

func TestAuthorizedFailsClosedOnUnreadableState(t *testing.T) {
	// An access-control gate fails CLOSED: a corrupt/unreadable state file
	// denies rather than silently dropping enforcement.
	corrupt := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if authorized(Options{AllowState: corrupt, Session: "demo"}, "u1") {
		t.Fatal("corrupt state must fail closed (deny), not authorize")
	}
	// A missing file resolves to empty state with no allows → still deny.
	if authorized(Options{AllowState: "/nonexistent/state.json", Session: "demo"}, "u1") {
		t.Fatal("missing state with no allows must deny")
	}
}
