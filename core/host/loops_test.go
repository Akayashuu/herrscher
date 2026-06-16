package host

import (
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher/core/internal/health"
)

func TestStatusContent(t *testing.T) {
	tests := []struct {
		name         string
		instanceID   string
		online       bool
		wantSubstr   string
		wantNoSubstr string
	}{
		{
			name:       "online-namespaced",
			instanceID: "alice",
			online:     true,
			wantSubstr: "[alice]",
		},
		{
			name:         "online-legacy-no-prefix",
			instanceID:   "",
			online:       true,
			wantSubstr:   "online",
			wantNoSubstr: "[]",
		},
		{
			name:       "offline-namespaced",
			instanceID: "bob",
			online:     false,
			wantSubstr: "[bob]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := health.HealthSnapshot{Online: tt.online, UptimeS: 5, PingMS: 12, Sessions: 2}
			got := statusContent(tt.instanceID, snap)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Fatalf("statusContent = %q, want substring %q", got, tt.wantSubstr)
			}
			if tt.wantNoSubstr != "" && strings.Contains(got, tt.wantNoSubstr) {
				t.Fatalf("statusContent = %q, must not contain %q", got, tt.wantNoSubstr)
			}
		})
	}
}
