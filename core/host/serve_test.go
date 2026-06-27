package host

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Herrscherd/herrscher/core/internal/obs"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

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
