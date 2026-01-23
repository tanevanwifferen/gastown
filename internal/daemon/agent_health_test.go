package daemon

import (
	"testing"
	"time"
)

func TestAgentStatus_Constants(t *testing.T) {
	// Verify status constants are correct strings
	tests := []struct {
		status   AgentStatus
		expected string
	}{
		{StatusHealthy, "healthy"},
		{StatusNeedsRespawn, "needs_respawn"},
		{StatusWaitingRespawn, "waiting_respawn"},
		{StatusStuck, "stuck"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.expected {
			t.Errorf("AgentStatus %q != expected %q", tt.status, tt.expected)
		}
	}
}

func TestAgentHealthConfig_Defaults(t *testing.T) {
	// Test that default constants have sensible values
	if DefaultStuckThreshold != 15*time.Minute {
		t.Errorf("DefaultStuckThreshold = %v, want 15m", DefaultStuckThreshold)
	}

	if DefaultRespawnDelay != 30*time.Second {
		t.Errorf("DefaultRespawnDelay = %v, want 30s", DefaultRespawnDelay)
	}

	if CriticalStuckThreshold != 30*time.Minute {
		t.Errorf("CriticalStuckThreshold = %v, want 30m", CriticalStuckThreshold)
	}
}

func TestBuildPatrolAgentBeadID(t *testing.T) {
	d := &Daemon{
		config: &Config{TownRoot: "/test/town"},
	}

	tests := []struct {
		role     string
		rigName  string
		expected string
	}{
		{"deacon", "", "hq-deacon"},
		{"witness", "gastown", "gt-gastown-witness"},
		{"refinery", "gastown", "gt-gastown-refinery"},
		{"witness", "beads", "bd-beads-witness"},
		{"refinery", "beads", "bd-beads-refinery"},
		{"unknown", "", ""},
	}

	for _, tt := range tests {
		result := d.buildPatrolAgentBeadID(tt.role, tt.rigName)
		if result != tt.expected {
			t.Errorf("buildPatrolAgentBeadID(%q, %q) = %q, want %q",
				tt.role, tt.rigName, result, tt.expected)
		}
	}
}

func TestGetRigPrefix(t *testing.T) {
	d := &Daemon{
		config: &Config{TownRoot: "/test/town"},
	}

	tests := []struct {
		rigName  string
		expected string
	}{
		{"gastown", "gt"},
		{"beads", "bd"},
		{"unknown", "gt"}, // Default fallback
	}

	for _, tt := range tests {
		result := d.getRigPrefix(tt.rigName)
		if result != tt.expected {
			t.Errorf("getRigPrefix(%q) = %q, want %q",
				tt.rigName, result, tt.expected)
		}
	}
}

func TestAgentHealthResult_Fields(t *testing.T) {
	// Test that AgentHealthResult fields work as expected
	now := time.Now()
	result := AgentHealthResult{
		Status:        StatusStuck,
		SessionAlive:  true,
		HeartbeatAge:  20 * time.Minute,
		LastHeartbeat: now.Add(-20 * time.Minute),
		Message:       "test message",
	}

	if result.Status != StatusStuck {
		t.Errorf("Status = %v, want %v", result.Status, StatusStuck)
	}

	if !result.SessionAlive {
		t.Error("SessionAlive = false, want true")
	}

	if result.HeartbeatAge != 20*time.Minute {
		t.Errorf("HeartbeatAge = %v, want 20m", result.HeartbeatAge)
	}

	if result.Message != "test message" {
		t.Errorf("Message = %q, want %q", result.Message, "test message")
	}
}
