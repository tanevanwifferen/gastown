package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/swarm"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Swarm command flags
var (
	swarmEpic       string
	swarmTasks      []string
	swarmWorkers    []string
	swarmStart      bool
	swarmStatusJSON bool
	swarmListRig    string
	swarmListStatus string
	swarmListJSON   bool
	swarmTarget     string
)

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Manage multi-agent swarms",
	Long: `Manage coordinated multi-agent work units (swarms).

A swarm coordinates multiple polecats working on related tasks from a shared
base commit. Work is merged to an integration branch, then landed to main.`,
}

var swarmCreateCmd = &cobra.Command{
	Use:   "create <rig>",
	Short: "Create a new swarm",
	Long: `Create a new swarm in a rig.

Creates a swarm that coordinates multiple polecats working on tasks from
a beads epic. All workers branch from the same base commit.

Examples:
  gt swarm create gastown --epic gt-abc --worker Toast --worker Nux
  gt swarm create gastown --epic gt-abc --worker Toast --start`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmCreate,
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status <swarm-id>",
	Short: "Show swarm status",
	Long: `Show detailed status for a swarm.

Displays swarm metadata, task progress, worker assignments, and integration
branch status.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmStatus,
}

var swarmListCmd = &cobra.Command{
	Use:   "list [rig]",
	Short: "List swarms",
	Long: `List swarms, optionally filtered by rig or status.

Examples:
  gt swarm list
  gt swarm list gastown
  gt swarm list --status=active
  gt swarm list gastown --status=landed`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSwarmList,
}

var swarmLandCmd = &cobra.Command{
	Use:   "land <swarm-id>",
	Short: "Land a swarm to main",
	Long: `Manually trigger landing for a completed swarm.

Merges the integration branch to the target branch (usually main).
Normally this is done automatically by the Refinery.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmLand,
}

var swarmCancelCmd = &cobra.Command{
	Use:   "cancel <swarm-id>",
	Short: "Cancel a swarm",
	Long: `Cancel an active swarm.

Marks the swarm as cancelled and optionally cleans up branches.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmCancel,
}

var swarmStartCmd = &cobra.Command{
	Use:   "start <swarm-id>",
	Short: "Start a created swarm",
	Long: `Start a swarm that was created without --start.

Transitions the swarm from 'created' to 'active' state.`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmStart,
}

func init() {
	// Create flags
	swarmCreateCmd.Flags().StringVar(&swarmEpic, "epic", "", "Beads epic ID for this swarm (required)")
	swarmCreateCmd.Flags().StringSliceVar(&swarmWorkers, "worker", nil, "Polecat names to assign (repeatable)")
	swarmCreateCmd.Flags().BoolVar(&swarmStart, "start", false, "Start swarm immediately after creation")
	swarmCreateCmd.Flags().StringVar(&swarmTarget, "target", "main", "Target branch for landing")
	swarmCreateCmd.MarkFlagRequired("epic")

	// Status flags
	swarmStatusCmd.Flags().BoolVar(&swarmStatusJSON, "json", false, "Output as JSON")

	// List flags
	swarmListCmd.Flags().StringVar(&swarmListStatus, "status", "", "Filter by status (active, landed, cancelled, failed)")
	swarmListCmd.Flags().BoolVar(&swarmListJSON, "json", false, "Output as JSON")

	// Add subcommands
	swarmCmd.AddCommand(swarmCreateCmd)
	swarmCmd.AddCommand(swarmStartCmd)
	swarmCmd.AddCommand(swarmStatusCmd)
	swarmCmd.AddCommand(swarmListCmd)
	swarmCmd.AddCommand(swarmLandCmd)
	swarmCmd.AddCommand(swarmCancelCmd)

	rootCmd.AddCommand(swarmCmd)
}

// SwarmStore manages persistent swarm state.
type SwarmStore struct {
	path   string
	Swarms map[string]*swarm.Swarm `json:"swarms"`
}

// LoadSwarmStore loads swarm state from disk.
func LoadSwarmStore(rigPath string) (*SwarmStore, error) {
	storePath := filepath.Join(rigPath, ".gastown", "swarms.json")
	store := &SwarmStore{
		path:   storePath,
		Swarms: make(map[string]*swarm.Swarm),
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, store); err != nil {
		return nil, err
	}
	store.path = storePath

	return store, nil
}

// Save persists swarm state to disk.
func (s *SwarmStore) Save() error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

// getSwarmRig gets a rig by name.
func getSwarmRig(rigName string) (*rig.Rig, string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return nil, "", fmt.Errorf("rig '%s' not found", rigName)
	}

	return r, townRoot, nil
}

// getAllRigs returns all discovered rigs.
func getAllRigs() ([]*rig.Rig, string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		return nil, "", err
	}

	return rigs, townRoot, nil
}

func runSwarmCreate(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	r, _, err := getSwarmRig(rigName)
	if err != nil {
		return err
	}

	// Load swarm store
	store, err := LoadSwarmStore(r.Path)
	if err != nil {
		return fmt.Errorf("loading swarm store: %w", err)
	}

	// Check if swarm already exists
	if _, exists := store.Swarms[swarmEpic]; exists {
		return fmt.Errorf("swarm for epic '%s' already exists", swarmEpic)
	}

	// Create swarm manager to use its Create logic
	mgr := swarm.NewManager(r)
	sw, err := mgr.Create(swarmEpic, swarmWorkers, swarmTarget)
	if err != nil {
		return fmt.Errorf("creating swarm: %w", err)
	}

	// Start if requested
	if swarmStart {
		if err := mgr.Start(swarmEpic); err != nil {
			return fmt.Errorf("starting swarm: %w", err)
		}
	}

	// Get the updated swarm
	sw, _ = mgr.GetSwarm(swarmEpic)

	// Save to store
	store.Swarms[swarmEpic] = sw
	if err := store.Save(); err != nil {
		return fmt.Errorf("saving swarm store: %w", err)
	}

	// Output
	fmt.Printf("%s Created swarm %s\n\n", style.Bold.Render("✓"), sw.ID)
	fmt.Printf("  Epic:        %s\n", sw.EpicID)
	fmt.Printf("  Rig:         %s\n", sw.RigName)
	fmt.Printf("  Base commit: %s\n", truncate(sw.BaseCommit, 8))
	fmt.Printf("  Integration: %s\n", sw.Integration)
	fmt.Printf("  Target:      %s\n", sw.TargetBranch)
	fmt.Printf("  State:       %s\n", sw.State)
	fmt.Printf("  Workers:     %s\n", strings.Join(sw.Workers, ", "))
	fmt.Printf("  Tasks:       %d\n", len(sw.Tasks))

	if !swarmStart {
		fmt.Printf("\n  %s\n", style.Dim.Render("Use --start or 'gt swarm start' to activate"))
	}

	return nil
}

func runSwarmStart(cmd *cobra.Command, args []string) error {
	swarmID := args[0]

	// Find the swarm and its rig
	rigs, _, err := getAllRigs()
	if err != nil {
		return err
	}

	var store *SwarmStore
	var foundRig *rig.Rig

	for _, r := range rigs {
		s, err := LoadSwarmStore(r.Path)
		if err != nil {
			continue
		}

		if _, exists := s.Swarms[swarmID]; exists {
			store = s
			foundRig = r
			break
		}
	}

	if store == nil {
		return fmt.Errorf("swarm '%s' not found", swarmID)
	}

	sw := store.Swarms[swarmID]

	if sw.State != swarm.SwarmCreated {
		return fmt.Errorf("swarm is not in 'created' state (current: %s)", sw.State)
	}

	sw.State = swarm.SwarmActive
	sw.UpdatedAt = time.Now()

	if err := store.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	fmt.Printf("%s Swarm %s started\n", style.Bold.Render("✓"), swarmID)

	// Spawn sessions for workers with tasks
	if len(sw.Workers) > 0 && len(sw.Tasks) > 0 {
		fmt.Printf("\nSpawning workers...\n")
		if err := spawnSwarmWorkers(foundRig, sw); err != nil {
			fmt.Printf("Warning: failed to spawn some workers: %v\n", err)
		}
	}

	return nil
}

// spawnSwarmWorkers spawns sessions for swarm workers with task assignments.
func spawnSwarmWorkers(r *rig.Rig, sw *swarm.Swarm) error {
	t := tmux.NewTmux()
	sessMgr := session.NewManager(t, r)
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit)

	// Pair workers with tasks (round-robin if more tasks than workers)
	workerIdx := 0
	for i, task := range sw.Tasks {
		if task.State != swarm.TaskPending {
			continue
		}

		if workerIdx >= len(sw.Workers) {
			break // No more workers
		}

		worker := sw.Workers[workerIdx]
		workerIdx++

		// Assign task to worker in swarm state
		sw.Tasks[i].Assignee = worker
		sw.Tasks[i].State = swarm.TaskAssigned

		// Update polecat state
		if err := polecatMgr.AssignIssue(worker, task.IssueID); err != nil {
			fmt.Printf("  Warning: couldn't assign %s to %s: %v\n", task.IssueID, worker, err)
			continue
		}

		// Check if already running
		running, _ := sessMgr.IsRunning(worker)
		if running {
			fmt.Printf("  %s already running, injecting task...\n", worker)
		} else {
			fmt.Printf("  Starting %s...\n", worker)
			if err := sessMgr.Start(worker, session.StartOptions{}); err != nil {
				fmt.Printf("  Warning: couldn't start %s: %v\n", worker, err)
				continue
			}
			// Wait for Claude to initialize
			time.Sleep(5 * time.Second)
		}

		// Inject work assignment
		context := fmt.Sprintf("[SWARM] You are part of swarm %s.\n\nAssigned task: %s\nTitle: %s\n\nWork on this task. When complete, commit and signal DONE.",
			sw.ID, task.IssueID, task.Title)
		if err := sessMgr.Inject(worker, context); err != nil {
			fmt.Printf("  Warning: couldn't inject to %s: %v\n", worker, err)
		} else {
			fmt.Printf("  %s → %s ✓\n", worker, task.IssueID)
		}
	}

	return nil
}

func runSwarmStatus(cmd *cobra.Command, args []string) error {
	swarmID := args[0]

	// Find the swarm across all rigs
	rigs, _, err := getAllRigs()
	if err != nil {
		return err
	}

	var foundSwarm *swarm.Swarm
	var foundRig *rig.Rig

	for _, r := range rigs {
		store, err := LoadSwarmStore(r.Path)
		if err != nil {
			continue
		}

		if sw, exists := store.Swarms[swarmID]; exists {
			foundSwarm = sw
			foundRig = r
			break
		}
	}

	if foundSwarm == nil {
		return fmt.Errorf("swarm '%s' not found", swarmID)
	}

	// JSON output
	if swarmStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(foundSwarm)
	}

	// Human-readable output
	sw := foundSwarm
	summary := sw.Summary()

	fmt.Printf("%s %s\n\n", style.Bold.Render("Swarm:"), sw.ID)
	fmt.Printf("  Rig:         %s\n", foundRig.Name)
	fmt.Printf("  Epic:        %s\n", sw.EpicID)
	fmt.Printf("  State:       %s\n", stateStyle(sw.State))
	fmt.Printf("  Created:     %s\n", sw.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Updated:     %s\n", sw.UpdatedAt.Format(time.RFC3339))
	fmt.Printf("  Base commit: %s\n", truncate(sw.BaseCommit, 8))
	fmt.Printf("  Integration: %s\n", sw.Integration)
	fmt.Printf("  Target:      %s\n", sw.TargetBranch)

	fmt.Printf("\n%s\n", style.Bold.Render("Workers:"))
	if len(sw.Workers) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(none assigned)"))
	} else {
		for _, w := range sw.Workers {
			fmt.Printf("  • %s\n", w)
		}
	}

	fmt.Printf("\n%s %d%% (%d/%d tasks merged)\n",
		style.Bold.Render("Progress:"),
		sw.Progress(),
		summary.MergedTasks,
		summary.TotalTasks)

	fmt.Printf("\n%s\n", style.Bold.Render("Tasks:"))
	if len(sw.Tasks) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no tasks loaded)"))
	} else {
		for _, task := range sw.Tasks {
			status := taskStateIcon(task.State)
			assignee := ""
			if task.Assignee != "" {
				assignee = fmt.Sprintf(" [%s]", task.Assignee)
			}
			fmt.Printf("  %s %s: %s%s\n", status, task.IssueID, task.Title, assignee)
		}
	}

	if sw.Error != "" {
		fmt.Printf("\n%s %s\n", style.Bold.Render("Error:"), sw.Error)
	}

	return nil
}

func runSwarmList(cmd *cobra.Command, args []string) error {
	rigs, _, err := getAllRigs()
	if err != nil {
		return err
	}

	// Filter by rig if specified
	if len(args) > 0 {
		rigName := args[0]
		var filtered []*rig.Rig
		for _, r := range rigs {
			if r.Name == rigName {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("rig '%s' not found", rigName)
		}
		rigs = filtered
	}

	// Collect all swarms
	type swarmEntry struct {
		Swarm *swarm.Swarm
		Rig   string
	}
	var allSwarms []swarmEntry

	for _, r := range rigs {
		store, err := LoadSwarmStore(r.Path)
		if err != nil {
			continue
		}

		for _, sw := range store.Swarms {
			// Filter by status if specified
			if swarmListStatus != "" {
				if !matchesStatus(sw.State, swarmListStatus) {
					continue
				}
			}
			allSwarms = append(allSwarms, swarmEntry{Swarm: sw, Rig: r.Name})
		}
	}

	// JSON output
	if swarmListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allSwarms)
	}

	// Human-readable output
	if len(allSwarms) == 0 {
		fmt.Println("No swarms found.")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Swarms"))
	for _, entry := range allSwarms {
		sw := entry.Swarm
		summary := sw.Summary()
		fmt.Printf("  %s %s [%s]\n",
			stateStyle(sw.State),
			sw.ID,
			entry.Rig)
		fmt.Printf("    %d workers, %d/%d tasks merged (%d%%)\n",
			summary.WorkerCount,
			summary.MergedTasks,
			summary.TotalTasks,
			sw.Progress())
	}

	return nil
}

func runSwarmLand(cmd *cobra.Command, args []string) error {
	swarmID := args[0]

	// Find the swarm
	rigs, townRoot, err := getAllRigs()
	if err != nil {
		return err
	}

	var foundRig *rig.Rig
	var store *SwarmStore

	for _, r := range rigs {
		s, err := LoadSwarmStore(r.Path)
		if err != nil {
			continue
		}

		if _, exists := s.Swarms[swarmID]; exists {
			foundRig = r
			store = s
			break
		}
	}

	if foundRig == nil {
		return fmt.Errorf("swarm '%s' not found", swarmID)
	}

	sw := store.Swarms[swarmID]

	// Check state - allow merging or active
	if sw.State != swarm.SwarmMerging && sw.State != swarm.SwarmActive {
		return fmt.Errorf("swarm must be in 'active' or 'merging' state to land (current: %s)", sw.State)
	}

	// Create manager and land
	mgr := swarm.NewManager(foundRig)
	// Reload swarm into manager
	mgr.Create(sw.EpicID, sw.Workers, sw.TargetBranch)
	mgr.UpdateState(sw.ID, sw.State)

	fmt.Printf("Landing swarm %s to %s...\n", swarmID, sw.TargetBranch)

	// First, merge integration branch to main
	if err := mgr.LandToMain(swarmID); err != nil {
		return fmt.Errorf("landing swarm: %w", err)
	}

	// Execute full landing protocol (stop sessions, audit, cleanup)
	config := swarm.LandingConfig{
		TownRoot: townRoot,
	}
	result, err := mgr.ExecuteLanding(swarmID, config)
	if err != nil {
		return fmt.Errorf("landing protocol: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("landing failed: %s", result.Error)
	}

	// Update store
	sw.State = swarm.SwarmLanded
	sw.UpdatedAt = time.Now()
	if err := store.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	fmt.Printf("%s Swarm %s landed to %s\n", style.Bold.Render("✓"), swarmID, sw.TargetBranch)
	fmt.Printf("  Sessions stopped: %d\n", result.SessionsStopped)
	fmt.Printf("  Branches cleaned: %d\n", result.BranchesCleaned)
	return nil
}

func runSwarmCancel(cmd *cobra.Command, args []string) error {
	swarmID := args[0]

	// Find the swarm
	rigs, _, err := getAllRigs()
	if err != nil {
		return err
	}

	var store *SwarmStore

	for _, r := range rigs {
		s, err := LoadSwarmStore(r.Path)
		if err != nil {
			continue
		}

		if _, exists := s.Swarms[swarmID]; exists {
			store = s
			break
		}
	}

	if store == nil {
		return fmt.Errorf("swarm '%s' not found", swarmID)
	}

	sw := store.Swarms[swarmID]

	if sw.State.IsTerminal() {
		return fmt.Errorf("swarm already in terminal state: %s", sw.State)
	}

	sw.State = swarm.SwarmCancelled
	sw.UpdatedAt = time.Now()

	if err := store.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	fmt.Printf("%s Swarm %s cancelled\n", style.Bold.Render("✓"), swarmID)
	return nil
}

// Helper functions

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func stateStyle(state swarm.SwarmState) string {
	switch state {
	case swarm.SwarmCreated:
		return style.Dim.Render("○ created")
	case swarm.SwarmActive:
		return style.Bold.Render("● active")
	case swarm.SwarmMerging:
		return style.Bold.Render("⟳ merging")
	case swarm.SwarmLanded:
		return style.Bold.Render("✓ landed")
	case swarm.SwarmFailed:
		return style.Dim.Render("✗ failed")
	case swarm.SwarmCancelled:
		return style.Dim.Render("⊘ cancelled")
	default:
		return string(state)
	}
}

func taskStateIcon(state swarm.TaskState) string {
	switch state {
	case swarm.TaskPending:
		return style.Dim.Render("○")
	case swarm.TaskAssigned:
		return style.Dim.Render("◐")
	case swarm.TaskInProgress:
		return style.Bold.Render("●")
	case swarm.TaskReview:
		return style.Bold.Render("◉")
	case swarm.TaskMerged:
		return style.Bold.Render("✓")
	case swarm.TaskFailed:
		return style.Dim.Render("✗")
	default:
		return "?"
	}
}

func matchesStatus(state swarm.SwarmState, filter string) bool {
	filter = strings.ToLower(filter)
	switch filter {
	case "active":
		return state.IsActive()
	case "landed":
		return state == swarm.SwarmLanded
	case "cancelled":
		return state == swarm.SwarmCancelled
	case "failed":
		return state == swarm.SwarmFailed
	case "terminal":
		return state.IsTerminal()
	default:
		return string(state) == filter
	}
}
