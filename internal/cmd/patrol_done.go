package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/townlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

var patrolDoneCmd = &cobra.Command{
	Use:     "patrol-done",
	GroupID: GroupWork,
	Short:   "Terminate patrol agent after completing patrol cycle",
	Long: `Terminate a patrol agent (Deacon, Witness, Refinery) cleanly after completing a patrol cycle.

This command is for long-lived patrol agents that need to:
1. Record a patrol completion heartbeat (updates last_activity timestamp)
2. Log the completion to townlog and events feed
3. Kill their own session cleanly

Unlike 'gt done' (for polecats), this does NOT:
- Create merge requests
- Nuke worktrees (patrol agents persist)
- Submit work to merge queue

Allowed roles: deacon, witness, refinery
Rejected roles: polecat (use 'gt done'), crew (persistent workers)

Examples:
  gt patrol-done                         # Auto-detect role, terminate
  gt patrol-done --summary "All clear"   # Include patrol summary in heartbeat
  gt patrol-done --dry-run               # Show what would happen`,
	RunE: runPatrolDone,
}

var (
	patrolDoneSummary string
	patrolDoneDryRun  bool
)

// Allowed patrol roles
var patrolRoles = map[Role]bool{
	RoleDeacon:   true,
	RoleWitness:  true,
	RoleRefinery: true,
}

func init() {
	patrolDoneCmd.Flags().StringVar(&patrolDoneSummary, "summary", "", "Patrol summary for heartbeat")
	patrolDoneCmd.Flags().BoolVar(&patrolDoneDryRun, "dry-run", false, "Show what would happen without executing")

	rootCmd.AddCommand(patrolDoneCmd)
}

func runPatrolDone(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Get role info
	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}

	// Validate role - only patrol agents allowed
	if !patrolRoles[roleInfo.Role] {
		if roleInfo.Role == RolePolecat {
			return fmt.Errorf("gt patrol-done is for patrol agents only (you are a polecat)\nUse 'gt done' instead - polecats are ephemeral workers")
		}
		if roleInfo.Role == RoleCrew {
			return fmt.Errorf("gt patrol-done is for patrol agents only (you are crew)\nCrew workers persist intentionally and don't use patrol-done")
		}
		return fmt.Errorf("gt patrol-done requires role deacon, witness, or refinery (you are %s)", roleInfo.Role)
	}

	// Build agent identity
	agentID := buildPatrolAgentID(roleInfo)
	sessionName := buildPatrolSessionName(roleInfo)

	// Dry run mode - just show what would happen
	if patrolDoneDryRun {
		fmt.Printf("%s Patrol Done (dry-run)\n", style.Bold.Render("→"))
		fmt.Printf("  Role: %s\n", roleInfo.Role)
		fmt.Printf("  Agent ID: %s\n", agentID)
		fmt.Printf("  Session: %s\n", sessionName)
		if roleInfo.Rig != "" {
			fmt.Printf("  Rig: %s\n", roleInfo.Rig)
		}
		if patrolDoneSummary != "" {
			fmt.Printf("  Summary: %s\n", patrolDoneSummary)
		}
		fmt.Println()
		fmt.Println("Would do:")
		fmt.Println("  1. Record patrol completion heartbeat (update last_activity)")
		fmt.Println("  2. Log to townlog and events feed")
		fmt.Println("  3. Kill tmux session: " + sessionName)
		return nil
	}

	// Step 1: Record heartbeat (update last_activity on agent bead)
	fmt.Printf("%s Recording patrol completion heartbeat...\n", style.Bold.Render("→"))
	if err := recordPatrolHeartbeat(cwd, townRoot, roleInfo, patrolDoneSummary); err != nil {
		// Non-fatal - warn but continue with session termination
		style.PrintWarning("heartbeat recording failed: %v", err)
	} else {
		fmt.Printf("%s Heartbeat recorded\n", style.Bold.Render("✓"))
	}

	// Step 2: Log to townlog and events feed
	logger := townlog.NewLogger(townRoot)
	summary := patrolDoneSummary
	if summary == "" {
		summary = "patrol complete"
	}
	_ = logger.Log(townlog.EventPatrolComplete, agentID, summary)

	// Log to events feed
	payload := map[string]interface{}{
		"agent": agentID,
	}
	if roleInfo.Rig != "" {
		payload["rig"] = roleInfo.Rig
	}
	if patrolDoneSummary != "" {
		payload["summary"] = patrolDoneSummary
	}
	_ = events.LogFeed(events.TypePatrolComplete, agentID, payload)

	fmt.Printf("%s Logged patrol completion\n", style.Bold.Render("✓"))

	// Step 3: Kill our own session
	fmt.Printf("%s Terminating session: %s\n", style.Bold.Render("→"), sessionName)

	t := tmux.NewTmux()

	// Check if session exists
	exists, err := t.HasSession(sessionName)
	if err != nil {
		style.PrintWarning("checking session: %v", err)
	}

	if !exists {
		fmt.Printf("%s Session already terminated\n", style.Bold.Render("✓"))
		os.Exit(0)
	}

	// Kill session with process cleanup, excluding our own PID
	// This is the same pattern used by selfKillSession in done.go
	myPID := strconv.Itoa(os.Getpid())
	if err := t.KillSessionWithProcessesExcluding(sessionName, []string{myPID}); err != nil {
		// If session kill fails, fall through to os.Exit
		style.PrintWarning("session kill failed: %v", err)
	}

	// If KillSessionWithProcessesExcluding succeeded, we won't reach here
	// (the tmux session kill will terminate this process)
	fmt.Printf("%s Session terminated\n", style.Bold.Render("✓"))
	os.Exit(0)

	return nil // unreachable, but keeps compiler happy
}

// buildPatrolAgentID returns the agent identity string for patrol agents.
func buildPatrolAgentID(roleInfo RoleInfo) string {
	switch roleInfo.Role {
	case RoleDeacon:
		return "deacon/"
	case RoleWitness:
		if roleInfo.Rig != "" {
			return fmt.Sprintf("%s/witness", roleInfo.Rig)
		}
		return "witness"
	case RoleRefinery:
		if roleInfo.Rig != "" {
			return fmt.Sprintf("%s/refinery", roleInfo.Rig)
		}
		return "refinery"
	default:
		return string(roleInfo.Role)
	}
}

// buildPatrolSessionName returns the tmux session name for patrol agents.
func buildPatrolSessionName(roleInfo RoleInfo) string {
	switch roleInfo.Role {
	case RoleDeacon:
		return "gt-deacon"
	case RoleWitness:
		if roleInfo.Rig != "" {
			return fmt.Sprintf("gt-%s-witness", roleInfo.Rig)
		}
		return "gt-witness" // fallback, shouldn't happen
	case RoleRefinery:
		if roleInfo.Rig != "" {
			return fmt.Sprintf("gt-%s-refinery", roleInfo.Rig)
		}
		return "gt-refinery" // fallback, shouldn't happen
	default:
		return fmt.Sprintf("gt-%s", roleInfo.Role)
	}
}

// recordPatrolHeartbeat updates the agent bead's last_activity timestamp.
func recordPatrolHeartbeat(cwd, townRoot string, roleInfo RoleInfo, summary string) error {
	// Build context for agent bead lookup
	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}

	// Get agent bead ID
	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		return fmt.Errorf("no agent bead found for role %s", roleInfo.Role)
	}

	// Determine beads path based on role
	var beadsPath string
	switch roleInfo.Role {
	case RoleDeacon:
		beadsPath = townRoot
	default:
		// Witness and Refinery are rig-level
		if roleInfo.Rig != "" {
			beadsPath = townRoot + "/" + roleInfo.Rig
		} else {
			beadsPath = townRoot
		}
	}

	bd := beads.New(beadsPath)

	// Update last_activity label with current timestamp
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Use the bd CLI to set the label (same pattern as agent_state.go)
	// Format: last_activity:<timestamp>
	_, err := bd.Run("label", agentBeadID, "last_activity:"+timestamp)
	if err != nil {
		return fmt.Errorf("updating last_activity: %w", err)
	}

	// If summary provided, also update last_patrol_summary label
	if summary != "" {
		// Escape colons in summary to avoid label parsing issues
		safeSummary := strings.ReplaceAll(summary, ":", "_")
		_, err = bd.Run("label", agentBeadID, "last_patrol_summary:"+safeSummary)
		if err != nil {
			// Non-fatal - just warn
			style.PrintWarning("could not update last_patrol_summary: %v", err)
		}
	}

	return nil
}
