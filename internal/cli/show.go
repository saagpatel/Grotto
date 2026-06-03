package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newShowCmd builds `grotto show` — print a static waterfall for a stored trace.
// Behavior arrives in Phase 1.
func newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <trace-id>",
		Short: "Print a static waterfall for a stored trace",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("show: %w (arrives in Phase 1)", errNotImplemented)
		},
	}
}
