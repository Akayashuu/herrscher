package host

import (
	"testing"

	"github.com/Herrscherd/herrscher/core/internal/state"
)

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
			got, err := resolveInstanceID(st, tt.optID, tt.ownerID)
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
