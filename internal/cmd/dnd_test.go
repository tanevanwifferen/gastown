package cmd

import (
	"testing"
)

func TestAddressToAgentBeadID(t *testing.T) {
	townName := "ai"
	tests := []struct {
		address  string
		expected string
	}{
		{"mayor", "gt-ai-mayor"},
		{"deacon", "gt-ai-deacon"},
		{"gastown/witness", "gt-gastown-witness"},
		{"gastown/refinery", "gt-gastown-refinery"},
		{"gastown/alpha", "gt-gastown-polecat-alpha"},
		{"gastown/crew/max", "gt-gastown-crew-max"},
		{"beads/witness", "gt-beads-witness"},
		{"beads/beta", "gt-beads-polecat-beta"},
		// Invalid addresses should return empty string
		{"invalid", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := addressToAgentBeadID(tt.address, townName)
			if got != tt.expected {
				t.Errorf("addressToAgentBeadID(%q, %q) = %q, want %q", tt.address, townName, got, tt.expected)
			}
		})
	}
}
