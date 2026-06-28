package host

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/obs"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// kindGateway is a minimal Gateway whose Manifest reports a fixed kind, so the
// admin-routing tests can tell gateways apart.
type kindGateway struct{ kind string }

func (g kindGateway) Manifest() contracts.Manifest {
	return contracts.Manifest{Kind: g.kind, Category: contracts.CategoryGateway}
}
func (kindGateway) Post(context.Context, contracts.Conversation, string) (contracts.MessageID, error) {
	return "", nil
}
func (kindGateway) Reply(context.Context, contracts.Conversation, contracts.MessageID, string) (contracts.MessageID, error) {
	return "", nil
}
func (kindGateway) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}
func (kindGateway) Menu(context.Context, contracts.Conversation, contracts.MessageID, string, []contracts.Choice) error {
	return nil
}

// labeledAdmin is a no-op ChannelAdmin tagged with an id so a test can assert
// which gateway's admin was selected.
type labeledAdmin struct{ id string }

func (labeledAdmin) Kind(context.Context, string) (string, error)                { return "", nil }
func (labeledAdmin) CreateUnder(context.Context, string, string) (string, error) { return "", nil }
func (labeledAdmin) ForumPost(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (labeledAdmin) Archive(context.Context, string) error      { return nil }
func (labeledAdmin) Send(context.Context, string, string) error { return nil }

func TestAdminForHomeMatchesGatewayKind(t *testing.T) {
	discord := Deps{Gateway: kindGateway{"discord"}, Admin: labeledAdmin{"discord"}}
	terminal := Deps{Gateway: kindGateway{"terminal"}, Admin: labeledAdmin{"terminal"}}
	// Registration order puts Discord first, so firstAdmin would mis-route.
	gws := []Deps{discord, terminal}

	for _, tc := range []struct {
		homeType string
		want     string
	}{
		{"terminal", "terminal"},
		{"category", "discord"},
		{"forum", "discord"},
	} {
		got, ok := adminForHome(gws, state.HomeRef{ID: "h", Type: tc.homeType}).(labeledAdmin)
		if !ok || got.id != tc.want {
			t.Fatalf("home %q routed to %+v, want admin %q", tc.homeType, got, tc.want)
		}
	}
}

func TestAdminForHomeFallsBackToFirst(t *testing.T) {
	// No gateway matches a terminal home → fall back to the only admin present.
	gws := []Deps{{Gateway: kindGateway{"discord"}, Admin: labeledAdmin{"discord"}}}
	got, ok := adminForHome(gws, state.HomeRef{ID: "h", Type: "terminal"}).(labeledAdmin)
	if !ok || got.id != "discord" {
		t.Fatalf("fallback routed to %+v, want admin %q", got, "discord")
	}
}

func TestSeedTerminalHomeWhenForeground(t *testing.T) {
	st := state.NewState(filepath.Join(t.TempDir(), "state.json"))
	seedTerminalHome(st, true /* hasForeground */)
	if st.Home.Type != "terminal" || st.Home.ID == "" {
		t.Fatalf("home not seeded: %+v", st.Home)
	}
	// Does not overwrite an existing home.
	st2 := state.NewState(filepath.Join(t.TempDir(), "s2.json"))
	_ = st2.SetHome(state.HomeRef{ID: "disc", Type: "category"})
	seedTerminalHome(st2, true)
	if st2.Home.Type != "category" {
		t.Fatalf("existing home overwritten: %+v", st2.Home)
	}
}

func TestResolveInstanceID(t *testing.T) {
	tests := []struct {
		name        string
		stateID     string
		hasSessions bool
		optID       string
		ownerID     string
		wantID      string
		wantFrozen  string // expected persisted State.InstanceID after the call
		wantErr     bool
	}{
		{
			name:       "fresh-state-freezes-explicit",
			optID:      "alice",
			wantID:     "alice",
			wantFrozen: "alice",
		},
		{
			name:       "fresh-state-derives-from-owner",
			ownerID:    "343535234303787009",
			wantID:     "u03787009",
			wantFrozen: "u03787009",
		},
		{
			name:       "no-inputs-stays-legacy",
			wantID:     "",
			wantFrozen: "",
		},
		{
			name:       "existing-id-matches",
			stateID:    "alice",
			optID:      "alice",
			wantID:     "alice",
			wantFrozen: "alice",
		},
		{
			name:       "existing-id-differs-errors",
			stateID:    "alice",
			optID:      "bob",
			wantErr:    true,
			wantFrozen: "alice", // unchanged
		},
		{
			name:        "legacy-sessions-block-new-id",
			hasSessions: true,
			optID:       "alice",
			wantID:      "",
			wantFrozen:  "", // not frozen, sessions preserved as legacy
		},
		{
			name:    "invalid-explicit-errors",
			optID:   "Bad!",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := state.NewState(t.TempDir() + "/s.json")
			st.InstanceID = tt.stateID
			if tt.hasSessions {
				if err := st.AddSession(state.Session{Name: "old", ChannelID: "c"}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveInstanceID(st, tt.optID, tt.ownerID, obs.NewLogger(io.Discard, slog.LevelInfo))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if st.InstanceID != tt.wantFrozen {
					t.Fatalf("state id mutated on error: got %q want %q", st.InstanceID, tt.wantFrozen)
				}
				return
			}
			if got != tt.wantID {
				t.Fatalf("effective id = %q, want %q", got, tt.wantID)
			}
			if st.InstanceID != tt.wantFrozen {
				t.Fatalf("frozen id = %q, want %q", st.InstanceID, tt.wantFrozen)
			}
		})
	}
}
