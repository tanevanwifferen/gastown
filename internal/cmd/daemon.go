package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var daemonCmd = &cobra.Command{
	Use:     "daemon",
	GroupID: GroupServices,
	Short:   "Manage the Gas Town daemon",
	RunE:    requireSubcommand,
	Long: `Manage the Gas Town background daemon.

The daemon is a simple Go process that:
- Pokes agents periodically (heartbeat)
- Processes lifecycle requests (cycle, restart, shutdown)
- Restarts sessions when agents request cycling

The daemon is a "dumb scheduler" - all intelligence is in agents.`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	Long: `Start the Gas Town daemon in the background.

The daemon will run until stopped with 'gt daemon stop'.`,
	RunE: runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon",
	Long:  `Stop the running Gas Town daemon.`,
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	Long:  `Show the current status of the Gas Town daemon.`,
	RunE:  runDaemonStatus,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View daemon logs",
	Long:  `View the daemon log file.`,
	RunE:  runDaemonLogs,
}

var daemonRunCmd = &cobra.Command{
	Use:    "run",
	Short:  "Run daemon in foreground (internal)",
	Hidden: true,
	RunE:   runDaemonRun,
}

var daemonRestartPatrolsCmd = &cobra.Command{
	Use:   "restart-patrols",
	Short: "Restart patrol agents to pick up new code/formulas",
	Long: `Restart patrol agents (deacon, witnesses, refineries) to pick up new code or formulas.

This is useful after deploying a new gt binary or updating agent formulas.
The command kills the patrol agent sessions and the daemon will respawn them
on the next heartbeat.

Examples:
  gt daemon restart-patrols                  # Restart all patrol agents
  gt daemon restart-patrols --rig gastown    # Restart only gastown's witness/refinery
  gt daemon restart-patrols --deacon-only    # Restart only the deacon`,
	RunE: runDaemonRestartPatrols,
}

var (
	daemonLogLines           int
	daemonLogFollow          bool
	restartPatrolsRig        string
	restartPatrolsDeaconOnly bool
)

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonRunCmd)
	daemonCmd.AddCommand(daemonRestartPatrolsCmd)

	daemonLogsCmd.Flags().IntVarP(&daemonLogLines, "lines", "n", 50, "Number of lines to show")
	daemonLogsCmd.Flags().BoolVarP(&daemonLogFollow, "follow", "f", false, "Follow log output")

	daemonRestartPatrolsCmd.Flags().StringVar(&restartPatrolsRig, "rig", "", "Restart only the specified rig's witness/refinery")
	daemonRestartPatrolsCmd.Flags().BoolVar(&restartPatrolsDeaconOnly, "deacon-only", false, "Restart only the deacon")

	rootCmd.AddCommand(daemonCmd)
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Check if already running
	running, pid, err := daemon.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking daemon status: %w", err)
	}
	if running {
		return fmt.Errorf("daemon already running (PID %d)", pid)
	}

	// Start daemon in background
	// We use 'gt daemon run' as the actual daemon process
	gtPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}

	daemonCmd := exec.Command(gtPath, "daemon", "run")
	daemonCmd.Dir = townRoot

	// Detach from terminal
	daemonCmd.Stdin = nil
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil

	if err := daemonCmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	// Wait a moment for the daemon to initialize and acquire the lock
	time.Sleep(200 * time.Millisecond)

	// Verify it started
	running, pid, err = daemon.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking daemon status: %w", err)
	}
	if !running {
		return fmt.Errorf("daemon failed to start (check logs with 'gt daemon logs')")
	}

	// Check if our spawned process is the one that won the race.
	// If another concurrent start won, our process would have exited after
	// failing to acquire the lock, and the PID file would have a different PID.
	if pid != daemonCmd.Process.Pid {
		// Another daemon won the race - that's fine, report it
		fmt.Printf("%s Daemon already running (PID %d)\n", style.Bold.Render("●"), pid)
		return nil
	}

	fmt.Printf("%s Daemon started (PID %d)\n", style.Bold.Render("✓"), pid)
	return nil
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := daemon.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking daemon status: %w", err)
	}
	if !running {
		return fmt.Errorf("daemon is not running")
	}

	if err := daemon.StopDaemon(townRoot); err != nil {
		return fmt.Errorf("stopping daemon: %w", err)
	}

	fmt.Printf("%s Daemon stopped (was PID %d)\n", style.Bold.Render("✓"), pid)
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := daemon.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking daemon status: %w", err)
	}

	if running {
		fmt.Printf("%s Daemon is %s (PID %d)\n",
			style.Bold.Render("●"),
			style.Bold.Render("running"),
			pid)

		// Load state for more details
		state, err := daemon.LoadState(townRoot)
		if err == nil && !state.StartedAt.IsZero() {
			fmt.Printf("  Started: %s\n", state.StartedAt.Format("2006-01-02 15:04:05"))
			if !state.LastHeartbeat.IsZero() {
				fmt.Printf("  Last heartbeat: %s (#%d)\n",
					state.LastHeartbeat.Format("15:04:05"),
					state.HeartbeatCount)
			}

			// Check if binary is newer than process
			if binaryModTime, err := getBinaryModTime(); err == nil {
				fmt.Printf("  Binary: %s\n", binaryModTime.Format("2006-01-02 15:04:05"))
				if binaryModTime.After(state.StartedAt) {
					fmt.Printf("  %s Binary is newer than process - consider '%s'\n",
						style.Bold.Render("⚠"),
						style.Dim.Render("gt daemon stop && gt daemon start"))
				}
			}
		}
	} else {
		fmt.Printf("%s Daemon is %s\n",
			style.Dim.Render("○"),
			"not running")
		fmt.Printf("\nStart with: %s\n", style.Dim.Render("gt daemon start"))
	}

	return nil
}

// getBinaryModTime returns the modification time of the current executable
func getBinaryModTime() (time.Time, error) {
	exePath, err := os.Executable()
	if err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(exePath)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func runDaemonLogs(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	logFile := filepath.Join(townRoot, "daemon", "daemon.log")

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		return fmt.Errorf("no log file found at %s", logFile)
	}

	if daemonLogFollow {
		// Use tail -f for following
		tailCmd := exec.Command("tail", "-f", logFile)
		tailCmd.Stdout = os.Stdout
		tailCmd.Stderr = os.Stderr
		return tailCmd.Run()
	}

	// Use tail -n for last N lines
	tailCmd := exec.Command("tail", "-n", fmt.Sprintf("%d", daemonLogLines), logFile)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	return tailCmd.Run()
}

func runDaemonRun(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := daemon.DefaultConfig(townRoot)
	d, err := daemon.New(config)
	if err != nil {
		return fmt.Errorf("creating daemon: %w", err)
	}

	return d.Run()
}

func runDaemonRestartPatrols(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	tm := tmux.NewTmux()
	if !tm.IsAvailable() {
		return fmt.Errorf("tmux is not available")
	}

	var sessionsToKill []string

	// Add deacon session unless --rig is specified without --deacon-only being false
	if !restartPatrolsDeaconOnly || restartPatrolsRig == "" {
		// Kill deacon if: no flags, --deacon-only, or --rig without --deacon-only
		if restartPatrolsRig == "" || restartPatrolsDeaconOnly {
			sessionsToKill = append(sessionsToKill, session.DeaconSessionName())
		}
	}

	// Add rig-specific sessions unless --deacon-only
	if !restartPatrolsDeaconOnly {
		var rigs []string
		if restartPatrolsRig != "" {
			// Specific rig
			rigs = []string{restartPatrolsRig}
		} else {
			// All rigs
			rigs = getKnownRigsFromTown(townRoot)
		}

		for _, rig := range rigs {
			sessionsToKill = append(sessionsToKill, session.WitnessSessionName(rig))
			sessionsToKill = append(sessionsToKill, session.RefinerySessionName(rig))
		}
	}

	if len(sessionsToKill) == 0 {
		fmt.Println("No patrol agents to restart")
		return nil
	}

	// Kill each session
	killed := 0
	for _, sess := range sessionsToKill {
		hasSession, err := tm.HasSession(sess)
		if err != nil {
			fmt.Printf("  %s Error checking %s: %v\n", style.Dim.Render("?"), sess, err)
			continue
		}
		if !hasSession {
			fmt.Printf("  %s %s (not running)\n", style.Dim.Render("-"), sess)
			continue
		}

		if err := tm.KillSessionWithProcesses(sess); err != nil {
			fmt.Printf("  %s Error killing %s: %v\n", style.Bold.Render("✗"), sess, err)
		} else {
			fmt.Printf("  %s Killed %s\n", style.Bold.Render("✓"), sess)
			killed++
		}
	}

	if killed > 0 {
		fmt.Printf("\n%s Killed %d session(s). Daemon will respawn on next heartbeat.\n",
			style.Bold.Render("✓"), killed)
	} else {
		fmt.Println("\nNo sessions were running.")
	}

	return nil
}

// getKnownRigsFromTown returns list of registered rig names from mayor/rigs.json.
func getKnownRigsFromTown(townRoot string) []string {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return nil
	}

	var parsed struct {
		Rigs map[string]interface{} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}

	var rigs []string
	for name := range parsed.Rigs {
		rigs = append(rigs, name)
	}
	return rigs
}
