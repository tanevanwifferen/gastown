package daemon

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/steveyegge/gastown/internal/events"
)

// AgentStatus represents the health status of an agent.
type AgentStatus string

const (
	// StatusHealthy means the agent is running and making progress.
	StatusHealthy AgentStatus = "healthy"

	// StatusNeedsRespawn means the agent session is dead and needs restart.
	StatusNeedsRespawn AgentStatus = "needs_respawn"

	// StatusWaitingRespawn means the session is dead but respawn delay hasn't elapsed.
	StatusWaitingRespawn AgentStatus = "waiting_respawn"

	// StatusStuck means the agent session is alive but not making progress.
	StatusStuck AgentStatus = "stuck"
)

// AgentHealthConfig holds configuration for agent health checks.
type AgentHealthConfig struct {
	// Role is the agent role (deacon, witness, refinery).
	Role string

	// RigName is the rig name (empty for town-level agents like deacon).
	RigName string

	// SessionName is the tmux session name for this agent.
	SessionName string

	// StuckThreshold is how long without a heartbeat before considered stuck.
	// Default: 15 minutes for patrol agents.
	StuckThreshold time.Duration

	// RespawnDelay is how long to wait after session death before respawning.
	// This prevents rapid restart loops if the agent keeps crashing.
	// Default: 30 seconds.
	RespawnDelay time.Duration

	// LastDeath tracks when the session was last detected as dead.
	// Used to enforce RespawnDelay.
	LastDeath time.Time
}

// DefaultStuckThreshold is the default duration before an agent is considered stuck.
// Patrol agents should complete their cycles within this time.
const DefaultStuckThreshold = 15 * time.Minute

// DefaultRespawnDelay is the minimum time to wait between respawns.
const DefaultRespawnDelay = 30 * time.Second

// CriticalStuckThreshold is the duration after which a stuck agent should be killed.
// Beyond this point, the agent is likely unrecoverable and needs a fresh start.
const CriticalStuckThreshold = 30 * time.Minute

// AgentHealthResult holds the result of a health check.
type AgentHealthResult struct {
	Status        AgentStatus
	SessionAlive  bool
	HeartbeatAge  time.Duration
	LastHeartbeat time.Time
	Message       string
}

// checkAgentHealth checks the health of an agent by examining session state and heartbeat staleness.
// Returns the agent's health status based on:
// - Session existence (is tmux session alive?)
// - Heartbeat staleness (has the agent completed a patrol cycle recently?)
func (d *Daemon) checkAgentHealth(config *AgentHealthConfig) AgentHealthResult {
	result := AgentHealthResult{}

	// Check if tmux session exists
	sessionAlive, err := d.tmux.HasSession(config.SessionName)
	if err != nil {
		d.logger.Printf("Error checking session %s: %v", config.SessionName, err)
		result.Status = StatusHealthy // Assume healthy on error
		result.Message = fmt.Sprintf("error checking session: %v", err)
		return result
	}
	result.SessionAlive = sessionAlive

	// Get last heartbeat for this agent
	lastHeartbeat := d.getLastPatrolCompleted(config.Role, config.RigName)
	if !lastHeartbeat.IsZero() {
		result.LastHeartbeat = lastHeartbeat
		result.HeartbeatAge = time.Since(lastHeartbeat)
	}

	// Determine status based on session state and heartbeat
	switch {
	case !sessionAlive && d.respawnDelayElapsed(config):
		result.Status = StatusNeedsRespawn
		result.Message = "session dead, respawn delay elapsed"

	case !sessionAlive:
		result.Status = StatusWaitingRespawn
		result.Message = fmt.Sprintf("session dead, waiting for respawn delay (%v remaining)",
			config.RespawnDelay-time.Since(config.LastDeath))

	case sessionAlive && result.HeartbeatAge > config.StuckThreshold:
		// Session is alive but heartbeat is stale - agent is stuck
		// But first, check if this is a first-startup scenario (no heartbeat yet)
		if lastHeartbeat.IsZero() {
			result.Status = StatusHealthy
			result.Message = "no heartbeat yet (first startup)"
		} else {
			result.Status = StatusStuck
			result.Message = fmt.Sprintf("session alive but heartbeat stale (%v old, threshold %v)",
				result.HeartbeatAge.Round(time.Minute), config.StuckThreshold)
		}

	default:
		result.Status = StatusHealthy
		if result.HeartbeatAge > 0 {
			result.Message = fmt.Sprintf("healthy, last heartbeat %v ago", result.HeartbeatAge.Round(time.Minute))
		} else {
			result.Message = "healthy"
		}
	}

	return result
}

// respawnDelayElapsed checks if the respawn delay has passed since last death.
func (d *Daemon) respawnDelayElapsed(config *AgentHealthConfig) bool {
	if config.LastDeath.IsZero() {
		return true // No recorded death, allow respawn
	}
	return time.Since(config.LastDeath) >= config.RespawnDelay
}

// getLastPatrolCompleted retrieves the last patrol completion time for an agent.
// Uses the agent bead's last_activity label (updated by gt patrol-done).
func (d *Daemon) getLastPatrolCompleted(role, rigName string) time.Time {
	// Build agent bead ID based on role
	agentBeadID := d.buildPatrolAgentBeadID(role, rigName)
	if agentBeadID == "" {
		return time.Time{}
	}

	// Query the agent bead to verify it exists
	if _, err := d.getAgentBeadInfo(agentBeadID); err != nil {
		return time.Time{}
	}

	// Parse last_activity from labels
	return d.parseLastActivityFromBead(agentBeadID)
}

// buildPatrolAgentBeadID builds the agent bead ID for a patrol agent.
func (d *Daemon) buildPatrolAgentBeadID(role, rigName string) string {
	switch role {
	case "deacon":
		return "hq-deacon"
	case "witness":
		if rigName != "" {
			// Use rig prefix lookup
			prefix := d.getRigPrefix(rigName)
			return fmt.Sprintf("%s-%s-witness", prefix, rigName)
		}
		return ""
	case "refinery":
		if rigName != "" {
			prefix := d.getRigPrefix(rigName)
			return fmt.Sprintf("%s-%s-refinery", prefix, rigName)
		}
		return ""
	default:
		return ""
	}
}

// getRigPrefix looks up the prefix for a rig from the routes config.
func (d *Daemon) getRigPrefix(rigName string) string {
	// Use the beads package's GetPrefixForRig function
	// For now, default to common prefixes based on rig name
	switch rigName {
	case "gastown":
		return "gt"
	case "beads":
		return "bd"
	default:
		return "gt" // Default fallback
	}
}

// parseLastActivityFromBead parses the last_activity label from an agent bead.
func (d *Daemon) parseLastActivityFromBead(agentBeadID string) time.Time {
	// Use bd show to get the bead with labels
	cmd := exec.Command("bd", "show", agentBeadID, "--json")
	cmd.Dir = d.config.TownRoot

	output, err := cmd.Output()
	if err != nil {
		return time.Time{}
	}

	// Parse the JSON output
	var issues []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return time.Time{}
	}

	if len(issues) == 0 {
		return time.Time{}
	}

	// Look for last_activity label
	for _, label := range issues[0].Labels {
		if len(label) > 14 && label[:14] == "last_activity:" {
			timestamp := label[14:]
			t, err := time.Parse(time.RFC3339, timestamp)
			if err != nil {
				return time.Time{}
			}
			return t
		}
	}

	return time.Time{}
}

// handleStuckAgent handles an agent that is stuck (session alive but not progressing).
// For moderately stuck agents, it nudges. For critically stuck agents, it kills for respawn.
func (d *Daemon) handleStuckAgent(config *AgentHealthConfig, result AgentHealthResult) {
	d.logger.Printf("Agent %s is stuck (last heartbeat %v ago)",
		config.SessionName, result.HeartbeatAge.Round(time.Minute))

	// Emit stuck agent event
	_ = events.LogFeed(events.TypeAgentStuck, "daemon", map[string]interface{}{
		"role":          config.Role,
		"rig":           config.RigName,
		"session":       config.SessionName,
		"heartbeat_age": result.HeartbeatAge.String(),
	})

	// Determine action based on how stuck the agent is
	if result.HeartbeatAge > CriticalStuckThreshold {
		// Critically stuck - kill for respawn
		d.logger.Printf("Agent %s critically stuck (%v > %v) - killing for respawn",
			config.SessionName, result.HeartbeatAge.Round(time.Minute), CriticalStuckThreshold)

		if err := d.tmux.KillSessionWithProcesses(config.SessionName); err != nil {
			d.logger.Printf("Error killing stuck agent %s: %v", config.SessionName, err)
		} else {
			d.logger.Printf("Killed stuck agent %s, will respawn on next heartbeat", config.SessionName)
		}
	} else {
		// Moderately stuck - try nudging first
		d.logger.Printf("Agent %s moderately stuck - nudging to wake up", config.SessionName)
		nudgeMsg := fmt.Sprintf("HEALTH_CHECK: heartbeat stale (%v old), please respond to confirm responsiveness",
			result.HeartbeatAge.Round(time.Minute))
		if err := d.tmux.NudgeSession(config.SessionName, nudgeMsg); err != nil {
			d.logger.Printf("Error nudging stuck agent %s: %v", config.SessionName, err)
		}
	}
}

// checkPatrolAgentHealth performs health checks on all patrol agents.
// This is called from the daemon heartbeat loop.
func (d *Daemon) checkPatrolAgentHealth() {
	// Check Deacon health (if patrol enabled)
	if IsPatrolEnabled(d.patrolConfig, "deacon") {
		d.checkDeaconAgentHealth()
	}

	// Check Witness health for all rigs (if patrol enabled)
	if IsPatrolEnabled(d.patrolConfig, "witness") {
		d.checkWitnessAgentHealth()
	}

	// Check Refinery health for all rigs (if patrol enabled)
	if IsPatrolEnabled(d.patrolConfig, "refinery") {
		d.checkRefineryAgentHealth()
	}
}

// checkDeaconAgentHealth checks the Deacon's health using the new health check system.
func (d *Daemon) checkDeaconAgentHealth() {
	config := &AgentHealthConfig{
		Role:           "deacon",
		SessionName:    d.getDeaconSessionName(),
		StuckThreshold: DefaultStuckThreshold,
		RespawnDelay:   DefaultRespawnDelay,
	}

	result := d.checkAgentHealth(config)

	switch result.Status {
	case StatusStuck:
		d.handleStuckAgent(config, result)
	case StatusNeedsRespawn:
		d.logger.Printf("Deacon needs respawn: %s", result.Message)
		// ensureDeaconRunning() is already called in heartbeat, so just log
	case StatusWaitingRespawn:
		d.logger.Printf("Deacon waiting for respawn: %s", result.Message)
	case StatusHealthy:
		// Nothing to do
	}
}

// checkWitnessAgentHealth checks Witness health for all rigs.
func (d *Daemon) checkWitnessAgentHealth() {
	rigs := d.getKnownRigs()
	for _, rigName := range rigs {
		config := &AgentHealthConfig{
			Role:           "witness",
			RigName:        rigName,
			SessionName:    fmt.Sprintf("gt-%s-witness", rigName),
			StuckThreshold: DefaultStuckThreshold,
			RespawnDelay:   DefaultRespawnDelay,
		}

		result := d.checkAgentHealth(config)

		switch result.Status {
		case StatusStuck:
			d.handleStuckAgent(config, result)
		case StatusNeedsRespawn:
			d.logger.Printf("Witness for %s needs respawn: %s", rigName, result.Message)
		case StatusWaitingRespawn:
			d.logger.Printf("Witness for %s waiting for respawn: %s", rigName, result.Message)
		case StatusHealthy:
			// Nothing to do
		}
	}
}

// checkRefineryAgentHealth checks Refinery health for all rigs.
func (d *Daemon) checkRefineryAgentHealth() {
	rigs := d.getKnownRigs()
	for _, rigName := range rigs {
		config := &AgentHealthConfig{
			Role:           "refinery",
			RigName:        rigName,
			SessionName:    fmt.Sprintf("gt-%s-refinery", rigName),
			StuckThreshold: DefaultStuckThreshold,
			RespawnDelay:   DefaultRespawnDelay,
		}

		result := d.checkAgentHealth(config)

		switch result.Status {
		case StatusStuck:
			d.handleStuckAgent(config, result)
		case StatusNeedsRespawn:
			d.logger.Printf("Refinery for %s needs respawn: %s", rigName, result.Message)
		case StatusWaitingRespawn:
			d.logger.Printf("Refinery for %s waiting for respawn: %s", rigName, result.Message)
		case StatusHealthy:
			// Nothing to do
		}
	}
}
