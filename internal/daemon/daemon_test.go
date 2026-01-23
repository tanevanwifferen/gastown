package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	townRoot := "/tmp/test-town"
	config := DefaultConfig(townRoot)

	if config.HeartbeatInterval != 5*time.Minute {
		t.Errorf("expected HeartbeatInterval 5m, got %v", config.HeartbeatInterval)
	}
	if config.TownRoot != townRoot {
		t.Errorf("expected TownRoot %q, got %q", townRoot, config.TownRoot)
	}
	if config.LogFile != filepath.Join(townRoot, "daemon", "daemon.log") {
		t.Errorf("expected LogFile in daemon dir, got %q", config.LogFile)
	}
	if config.PidFile != filepath.Join(townRoot, "daemon", "daemon.pid") {
		t.Errorf("expected PidFile in daemon dir, got %q", config.PidFile)
	}
}

func TestStateFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	expected := filepath.Join(townRoot, "daemon", "state.json")
	result := StateFile(townRoot)

	if result != expected {
		t.Errorf("StateFile(%q) = %q, expected %q", townRoot, result, expected)
	}
}

func TestLoadState_NonExistent(t *testing.T) {
	// Create temp dir that doesn't have a state file
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state, err := LoadState(tmpDir)
	if err != nil {
		t.Errorf("LoadState should not error for missing file, got %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Running {
		t.Error("expected Running=false for empty state")
	}
	if state.PID != 0 {
		t.Errorf("expected PID=0 for empty state, got %d", state.PID)
	}
}

func TestLoadState_ExistingFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a state file
	startTime := time.Now().Truncate(time.Second)
	testState := &State{
		Running:        true,
		PID:            12345,
		StartedAt:      startTime,
		LastHeartbeat:  startTime,
		HeartbeatCount: 42,
	}

	data, err := json.MarshalIndent(testState, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Load and verify
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if !loaded.Running {
		t.Error("expected Running=true")
	}
	if loaded.PID != 12345 {
		t.Errorf("expected PID=12345, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 42 {
		t.Errorf("expected HeartbeatCount=42, got %d", loaded.HeartbeatCount)
	}
}

func TestLoadState_InvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create daemon directory with invalid JSON
	daemonDir := filepath.Join(tmpDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "state.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadState(tmpDir)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSaveState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	state := &State{
		Running:        true,
		PID:            9999,
		StartedAt:      time.Now(),
		LastHeartbeat:  time.Now(),
		HeartbeatCount: 100,
	}

	// SaveState should create daemon directory if needed
	if err := SaveState(tmpDir, state); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	// Verify file exists
	stateFile := StateFile(tmpDir)
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file should exist: %v", err)
	}

	// Verify contents
	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if loaded.PID != 9999 {
		t.Errorf("expected PID=9999, got %d", loaded.PID)
	}
	if loaded.HeartbeatCount != 100 {
		t.Errorf("expected HeartbeatCount=100, got %d", loaded.HeartbeatCount)
	}
}

func TestSaveLoadState_Roundtrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	original := &State{
		Running:        true,
		PID:            54321,
		StartedAt:      time.Now().Truncate(time.Second),
		LastHeartbeat:  time.Now().Truncate(time.Second),
		HeartbeatCount: 1000,
	}

	if err := SaveState(tmpDir, original); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	loaded, err := LoadState(tmpDir)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if loaded.Running != original.Running {
		t.Errorf("Running mismatch: got %v, want %v", loaded.Running, original.Running)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID mismatch: got %d, want %d", loaded.PID, original.PID)
	}
	if loaded.HeartbeatCount != original.HeartbeatCount {
		t.Errorf("HeartbeatCount mismatch: got %d, want %d", loaded.HeartbeatCount, original.HeartbeatCount)
	}
	// Time comparison with truncation to handle JSON serialization
	if !loaded.StartedAt.Truncate(time.Second).Equal(original.StartedAt) {
		t.Errorf("StartedAt mismatch: got %v, want %v", loaded.StartedAt, original.StartedAt)
	}
}

func TestListPolecatWorktrees_SkipsHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()
	polecatsDir := filepath.Join(tmpDir, "some-rig", "polecats")

	if err := os.MkdirAll(filepath.Join(polecatsDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(polecatsDir, "furiosa"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatsDir, "not-a-dir.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		t.Fatalf("listPolecatWorktrees returned error: %v", err)
	}

	if slices.Contains(polecats, ".claude") {
		t.Fatalf("expected hidden dir .claude to be ignored, got %v", polecats)
	}
	if !slices.Contains(polecats, "furiosa") {
		t.Fatalf("expected furiosa to be included, got %v", polecats)
	}
}

// NOTE: TestIsWitnessSession removed - isWitnessSession function was deleted
// as part of ZFC cleanup. Witness poking is now Deacon's responsibility.

func TestLifecycleAction_Constants(t *testing.T) {
	// Verify constants have expected string values
	if ActionCycle != "cycle" {
		t.Errorf("expected ActionCycle='cycle', got %q", ActionCycle)
	}
	if ActionRestart != "restart" {
		t.Errorf("expected ActionRestart='restart', got %q", ActionRestart)
	}
	if ActionShutdown != "shutdown" {
		t.Errorf("expected ActionShutdown='shutdown', got %q", ActionShutdown)
	}
}

func TestLifecycleRequest_Serialization(t *testing.T) {
	request := &LifecycleRequest{
		From:      "mayor",
		Action:    ActionCycle,
		Timestamp: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded LifecycleRequest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.From != request.From {
		t.Errorf("From mismatch: got %q, want %q", loaded.From, request.From)
	}
	if loaded.Action != request.Action {
		t.Errorf("Action mismatch: got %q, want %q", loaded.Action, request.Action)
	}
}

func TestGetRespawnConfig_KnownRoles(t *testing.T) {
	tests := []struct {
		role           string
		expectedDelay  time.Duration
		expectedTrigger string
	}{
		{"deacon", 5 * time.Minute, "always"},
		{"witness", 5 * time.Minute, "always"},
		{"refinery", 0, "mq-not-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			cfg := GetRespawnConfig(tt.role)
			if cfg.Delay != tt.expectedDelay {
				t.Errorf("Delay for %s: got %v, want %v", tt.role, cfg.Delay, tt.expectedDelay)
			}
			if cfg.Trigger != tt.expectedTrigger {
				t.Errorf("Trigger for %s: got %q, want %q", tt.role, cfg.Trigger, tt.expectedTrigger)
			}
		})
	}
}

func TestGetRespawnConfig_UnknownRole(t *testing.T) {
	cfg := GetRespawnConfig("unknown-role")
	// Should return conservative defaults
	if cfg.Delay != 5*time.Minute {
		t.Errorf("Default Delay: got %v, want 5m", cfg.Delay)
	}
	if cfg.Trigger != "always" {
		t.Errorf("Default Trigger: got %q, want 'always'", cfg.Trigger)
	}
}

func TestState_GetAgentState(t *testing.T) {
	state := &State{}

	// First call should create the agent
	agent := state.GetAgentState("deacon")
	if agent == nil {
		t.Fatal("GetAgentState should not return nil")
	}

	// Second call should return the same agent
	agent2 := state.GetAgentState("deacon")
	if agent != agent2 {
		t.Error("GetAgentState should return same instance")
	}

	// Different agent should return different instance
	agent3 := state.GetAgentState("gastown/witness")
	if agent == agent3 {
		t.Error("Different agents should have different instances")
	}
}

func TestState_ScheduleRespawn(t *testing.T) {
	state := &State{}
	before := time.Now()

	state.ScheduleRespawn("deacon", "gt-deacon", "deacon", "crash")

	agent := state.GetAgentState("deacon")
	if agent.Session != "gt-deacon" {
		t.Errorf("Session: got %q, want 'gt-deacon'", agent.Session)
	}
	if agent.ExitReason != "crash" {
		t.Errorf("ExitReason: got %q, want 'crash'", agent.ExitReason)
	}
	if agent.LastExitedAt == nil {
		t.Fatal("LastExitedAt should be set")
	}
	if agent.RespawnScheduledAt == nil {
		t.Fatal("RespawnScheduledAt should be set")
	}

	// Verify respawn is scheduled after the delay
	cfg := GetRespawnConfig("deacon")
	expectedRespawnAt := agent.LastExitedAt.Add(cfg.Delay)
	if !agent.RespawnScheduledAt.Equal(expectedRespawnAt) {
		t.Errorf("RespawnScheduledAt: got %v, want %v", agent.RespawnScheduledAt, expectedRespawnAt)
	}

	// Verify timestamps are reasonable
	if agent.LastExitedAt.Before(before) {
		t.Error("LastExitedAt should be after test start")
	}
}

func TestState_ClearRespawn(t *testing.T) {
	state := &State{}

	// Schedule a respawn first
	state.ScheduleRespawn("deacon", "gt-deacon", "deacon", "crash")
	agent := state.GetAgentState("deacon")
	if agent.RespawnScheduledAt == nil {
		t.Fatal("RespawnScheduledAt should be set before clear")
	}

	// Clear the respawn
	state.ClearRespawn("deacon", "gt-deacon-new")

	if agent.RespawnScheduledAt != nil {
		t.Error("RespawnScheduledAt should be nil after clear")
	}
	if agent.LastExitedAt != nil {
		t.Error("LastExitedAt should be nil after clear")
	}
	if agent.ExitReason != "" {
		t.Error("ExitReason should be empty after clear")
	}
	if agent.Session != "gt-deacon-new" {
		t.Errorf("Session should be updated: got %q, want 'gt-deacon-new'", agent.Session)
	}
}

func TestState_UpdatePatrolCompleted(t *testing.T) {
	state := &State{}
	before := time.Now()

	state.UpdatePatrolCompleted("gastown/witness")

	agent := state.GetAgentState("gastown/witness")
	if agent.LastPatrolCompleted == nil {
		t.Fatal("LastPatrolCompleted should be set")
	}
	if agent.LastPatrolCompleted.Before(before) {
		t.Error("LastPatrolCompleted should be after test start")
	}
}

func TestAgentState_Serialization(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	respawnAt := now.Add(5 * time.Minute)

	state := &State{
		Running: true,
		PID:     12345,
		Agents: map[string]*AgentState{
			"deacon": {
				Session:            "gt-deacon",
				RespawnScheduledAt: &respawnAt,
				LastExitedAt:       &now,
				ExitReason:         "cycle",
			},
		},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.Agents == nil {
		t.Fatal("Agents should not be nil")
	}
	agent, ok := loaded.Agents["deacon"]
	if !ok {
		t.Fatal("deacon agent should exist")
	}
	if agent.Session != "gt-deacon" {
		t.Errorf("Session: got %q, want 'gt-deacon'", agent.Session)
	}
	if agent.ExitReason != "cycle" {
		t.Errorf("ExitReason: got %q, want 'cycle'", agent.ExitReason)
	}
}
