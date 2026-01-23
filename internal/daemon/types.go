// Package daemon provides the town-level background service for Gas Town.
//
// The daemon is a simple Go process (not a Claude agent) that:
// 1. Pokes agents periodically (heartbeat)
// 2. Processes lifecycle requests (cycle, restart, shutdown)
// 3. Restarts sessions when agents request cycling
//
// The daemon is a "dumb scheduler" - all intelligence is in agents.
package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// Config holds daemon configuration.
type Config struct {
	// HeartbeatInterval is how often to poke agents.
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`

	// TownRoot is the Gas Town workspace root.
	TownRoot string `json:"town_root"`

	// LogFile is the path to the daemon log file.
	LogFile string `json:"log_file"`

	// PidFile is the path to the PID file.
	PidFile string `json:"pid_file"`
}

// DefaultConfig returns the default daemon configuration.
func DefaultConfig(townRoot string) *Config {
	daemonDir := filepath.Join(townRoot, "daemon")
	return &Config{
		HeartbeatInterval: 5 * time.Minute, // Deacon wakes on mail too, no need to poke often
		TownRoot:          townRoot,
		LogFile:           filepath.Join(daemonDir, "daemon.log"),
		PidFile:           filepath.Join(daemonDir, "daemon.pid"),
	}
}

// State represents the daemon's runtime state.
type State struct {
	// Running indicates if the daemon is running.
	Running bool `json:"running"`

	// PID is the process ID of the daemon.
	PID int `json:"pid"`

	// StartedAt is when the daemon started.
	StartedAt time.Time `json:"started_at"`

	// LastHeartbeat is when the last heartbeat completed.
	LastHeartbeat time.Time `json:"last_heartbeat"`

	// HeartbeatCount is how many heartbeats have completed.
	HeartbeatCount int64 `json:"heartbeat_count"`

	// Agents tracks per-agent state for respawn scheduling.
	// Key is the agent identifier (e.g., "deacon", "gastown/witness").
	Agents map[string]*AgentState `json:"agents,omitempty"`
}

// AgentState tracks the runtime state of a single agent for respawn scheduling.
type AgentState struct {
	// Session is the tmux session name for this agent.
	Session string `json:"session"`

	// LastPatrolCompleted is when the agent last completed a patrol cycle.
	// Used to detect stuck agents.
	LastPatrolCompleted *time.Time `json:"last_patrol_completed,omitempty"`

	// RespawnScheduledAt is when a respawn is scheduled to occur.
	// Nil if no respawn is scheduled.
	RespawnScheduledAt *time.Time `json:"respawn_scheduled_at,omitempty"`

	// LastExitedAt is when the agent's session was last detected as dead.
	// Used to calculate respawn delay.
	LastExitedAt *time.Time `json:"last_exited_at,omitempty"`

	// ExitReason is why the agent exited (if known).
	// Examples: "cycle", "crash", "stuck", "shutdown"
	ExitReason string `json:"exit_reason,omitempty"`
}

// GetAgentState returns the state for an agent, creating it if needed.
func (s *State) GetAgentState(agentID string) *AgentState {
	if s.Agents == nil {
		s.Agents = make(map[string]*AgentState)
	}
	if s.Agents[agentID] == nil {
		s.Agents[agentID] = &AgentState{}
	}
	return s.Agents[agentID]
}

// ScheduleRespawn schedules a respawn for an agent based on its role config.
func (s *State) ScheduleRespawn(agentID, session, role, exitReason string) {
	agent := s.GetAgentState(agentID)
	agent.Session = session
	agent.ExitReason = exitReason

	now := time.Now()
	agent.LastExitedAt = &now

	// Get respawn config for this role
	cfg := GetRespawnConfig(role)

	// Schedule respawn after the configured delay
	respawnAt := now.Add(cfg.Delay)
	agent.RespawnScheduledAt = &respawnAt
}

// ClearRespawn clears the scheduled respawn for an agent.
// Called when the agent is successfully restarted.
func (s *State) ClearRespawn(agentID, session string) {
	agent := s.GetAgentState(agentID)
	agent.Session = session
	agent.RespawnScheduledAt = nil
	agent.LastExitedAt = nil
	agent.ExitReason = ""
}

// UpdatePatrolCompleted updates the last patrol completed time for an agent.
func (s *State) UpdatePatrolCompleted(agentID string) {
	agent := s.GetAgentState(agentID)
	now := time.Now()
	agent.LastPatrolCompleted = &now
}

// StateFile returns the path to the state file.
func StateFile(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "state.json")
}

// LoadState loads daemon state from disk.
func LoadState(townRoot string) (*State, error) {
	stateFile := StateFile(townRoot)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SaveState saves daemon state to disk using atomic write.
func SaveState(townRoot string, state *State) error {
	stateFile := StateFile(townRoot)

	// Ensure daemon directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}

	return util.AtomicWriteJSON(stateFile, state)
}

// PatrolConfig holds configuration for a single patrol.
type PatrolConfig struct {
	// Enabled controls whether this patrol runs during heartbeat.
	Enabled bool `json:"enabled"`

	// Interval is how often to run this patrol (not used yet).
	Interval string `json:"interval,omitempty"`

	// Agent is the agent type for this patrol (not used yet).
	Agent string `json:"agent,omitempty"`
}

// PatrolsConfig holds configuration for all patrols.
type PatrolsConfig struct {
	Refinery *PatrolConfig `json:"refinery,omitempty"`
	Witness  *PatrolConfig `json:"witness,omitempty"`
	Deacon   *PatrolConfig `json:"deacon,omitempty"`
}

// DaemonPatrolConfig is the structure of mayor/daemon.json.
type DaemonPatrolConfig struct {
	Type      string         `json:"type"`
	Version   int            `json:"version"`
	Heartbeat *PatrolConfig  `json:"heartbeat,omitempty"`
	Patrols   *PatrolsConfig `json:"patrols,omitempty"`
}

// PatrolConfigFile returns the path to the patrol config file.
func PatrolConfigFile(townRoot string) string {
	return filepath.Join(townRoot, "mayor", "daemon.json")
}

// LoadPatrolConfig loads patrol configuration from mayor/daemon.json.
// Returns nil if the file doesn't exist or can't be parsed.
func LoadPatrolConfig(townRoot string) *DaemonPatrolConfig {
	configFile := PatrolConfigFile(townRoot)
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}

	var config DaemonPatrolConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return &config
}

// IsPatrolEnabled checks if a patrol is enabled in the config.
// Returns true if the config doesn't exist (default enabled for backwards compatibility).
func IsPatrolEnabled(config *DaemonPatrolConfig, patrol string) bool {
	if config == nil || config.Patrols == nil {
		return true // Default: enabled
	}

	switch patrol {
	case "refinery":
		if config.Patrols.Refinery != nil {
			return config.Patrols.Refinery.Enabled
		}
	case "witness":
		if config.Patrols.Witness != nil {
			return config.Patrols.Witness.Enabled
		}
	case "deacon":
		if config.Patrols.Deacon != nil {
			return config.Patrols.Deacon.Enabled
		}
	}
	return true // Default: enabled
}

// RespawnConfig holds configuration for controlled agent respawning.
// Each role can have different delays and triggers for when to respawn.
type RespawnConfig struct {
	// Delay is the time to wait before respawning an agent after it exits.
	Delay time.Duration `json:"delay"`

	// StuckThreshold is the heartbeat age that indicates an agent is stuck.
	// If an agent's heartbeat is older than this, it's considered stuck.
	StuckThreshold time.Duration `json:"stuck_threshold"`

	// Trigger controls when respawn should occur:
	// - "always": respawn immediately after delay expires
	// - "mq-not-empty": only respawn if merge queue has work waiting
	// - "work-available": only respawn if there's work to be assigned
	Trigger string `json:"trigger"`
}

// DefaultRespawnConfigs provides role-specific respawn settings.
// These balance responsiveness with resource efficiency.
var DefaultRespawnConfigs = map[string]RespawnConfig{
	"deacon": {
		Delay:          5 * time.Minute,
		StuckThreshold: 15 * time.Minute,
		Trigger:        "always",
	},
	"witness": {
		Delay:          5 * time.Minute,
		StuckThreshold: 10 * time.Minute,
		Trigger:        "always",
	},
	"refinery": {
		Delay:          0, // immediate
		StuckThreshold: 5 * time.Minute,
		Trigger:        "mq-not-empty", // only respawn if work waiting
	},
}

// GetRespawnConfig returns the respawn configuration for a role.
// Falls back to a sensible default if the role is not explicitly configured.
func GetRespawnConfig(role string) RespawnConfig {
	if cfg, ok := DefaultRespawnConfigs[role]; ok {
		return cfg
	}
	// Default for unknown roles: conservative delay, always respawn
	return RespawnConfig{
		Delay:          5 * time.Minute,
		StuckThreshold: 15 * time.Minute,
		Trigger:        "always",
	}
}

// LifecycleAction represents a lifecycle request action.
type LifecycleAction string

const (
	// ActionCycle restarts the session with handoff.
	ActionCycle LifecycleAction = "cycle"

	// ActionRestart does a fresh restart without handoff.
	ActionRestart LifecycleAction = "restart"

	// ActionShutdown terminates without restart.
	ActionShutdown LifecycleAction = "shutdown"
)

// LifecycleRequest represents a request from an agent to the daemon.
type LifecycleRequest struct {
	// From is the agent requesting the action (e.g., "mayor/", "gastown/witness").
	From string `json:"from"`

	// Action is what lifecycle action to perform.
	Action LifecycleAction `json:"action"`

	// Timestamp is when the request was made.
	Timestamp time.Time `json:"timestamp"`
}
