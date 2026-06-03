package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDiffCmd builds `grotto diff` — report per-span duration deltas between two
// traces. Behavior arrives in Phase 4.
func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <trace-a> <trace-b>",
		Short: "Compare two traces span-by-span",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("diff: %w (arrives in Phase 4)", errNotImplemented)
		},
	}
}
