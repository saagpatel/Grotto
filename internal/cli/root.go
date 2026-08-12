// Package cli wires the grotto command tree.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/store"
)

// version is the build version reported by `grotto --version`. It defaults to
// "dev" for plain `go build` and is overridden at release time via
// -ldflags "-X github.com/saagpatel/grotto/internal/cli.version=<v>" (see Makefile).
var version = "dev"

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
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Print a bare "grotto <version>" rather than cobra's default
	// "grotto version <version>" — terser and matches `grotto` invocation.
	root.SetVersionTemplate("grotto {{.Version}}\n")
	root.AddCommand(
		newRunCmd(),
		newMarkCmd(),
		newServeCmd(),
		newShowCmd(),
		newListCmd(),
		newDiffCmd(),
		newCompactionCmd(),
		newTUICmd(),
		newRedactPreviewCmd(),
	)
	return root
}

// openStore opens the local trace database at its default location (honoring
// GROTTO_DB). Callers own closing the returned store.
func openStore(ctx context.Context) (*store.Store, error) {
	path, err := store.DefaultDBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return st, nil
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
