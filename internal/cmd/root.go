// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/keepalive"
)

var rootCmd = &cobra.Command{
	Use:     "gt",
	Short:   "Gas Town - Multi-agent workspace manager",
	Version: Version,
	Long: `Gas Town (gt) manages multi-agent workspaces called rigs.

It coordinates agent spawning, work distribution, and communication
across distributed teams of AI agents working on shared codebases.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Signal agent activity by touching keepalive file
		// Build command path: gt status, gt mail send, etc.
		cmdPath := buildCommandPath(cmd)
		keepalive.TouchWithArgs(cmdPath, args)
	},
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Enable prefix matching for subcommands (e.g., "gt ref at" -> "gt refinery attach")
	cobra.EnablePrefixMatching = true

	// Global flags can be added here
	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")
}

// buildCommandPath walks the command hierarchy to build the full command path.
// For example: "gt mail send", "gt status", etc.
func buildCommandPath(cmd *cobra.Command) string {
	var parts []string
	for c := cmd; c != nil; c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}
