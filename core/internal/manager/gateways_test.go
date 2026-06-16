package manager

import (
	"slices"
	"testing"
)

func TestParseGateways(t *testing.T) {
	cases := []struct {
		name         string
		list         string
		terminalOnly bool
		want         []string
	}{
		{"default", "", false, []string{"discord"}},
		{"terminal only flag", "", true, []string{"terminal"}},
		{"explicit list", "discord,terminal", false, []string{"discord", "terminal"}},
		{"trims and drops empties", " discord , , terminal ", false, []string{"discord", "terminal"}},
		{"dedups", "discord,discord", false, []string{"discord"}},
		{"flag wins over empty list only", "discord,terminal", true, []string{"discord", "terminal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseGateways(tc.list, tc.terminalOnly)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseGateways(%q,%v) = %v, want %v", tc.list, tc.terminalOnly, got, tc.want)
			}
		})
	}
}
