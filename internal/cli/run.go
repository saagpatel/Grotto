package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newRunCmd builds `grotto run` — execute a command and capture its grotto
// marks into a single trace. Behavior arrives in Phase 1.
func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command and capture grotto marks into a trace",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("run: %w (arrives in Phase 1)", errNotImplemented)
		},
	}
}
