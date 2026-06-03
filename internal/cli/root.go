// Package cli wires the grotto command tree.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// errNotImplemented is returned by subcommands whose behavior lands in a later
// phase. Stubs wrap it with %w so callers can still match on it.
var errNotImplemented = errors.New("not implemented yet")

// NewRootCmd builds the root grotto command with all subcommands registered.
// Registration is explicit (no package-level init side effects) so the command
// tree is constructed deterministically and is testable in isolation.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "grotto",
		Short: "Render OpenTelemetry trace waterfalls for local commands",
		Long: "Grotto is a local-first CLI and TUI that renders OpenTelemetry " +
			"trace waterfalls for shell commands, build scripts, and test suites. " +
			"Everything stays on your machine and persists to local SQLite.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newRunCmd(),
		newMarkCmd(),
		newServeCmd(),
		newShowCmd(),
		newListCmd(),
		newDiffCmd(),
		newTUICmd(),
	)
	return root
}

// Execute runs the root command, printing any error to stderr, and returns the
// process exit code. It deliberately does not call os.Exit so that callers keep
// control of process teardown (and so it stays testable); main owns the exit.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "grotto:", err)
		return 1
	}
	return 0
}
