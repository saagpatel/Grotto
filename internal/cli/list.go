package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newListCmd builds `grotto list` — show recent traces in a table.
// Behavior arrives in Phase 4.
func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recent traces newest-first",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("list: %w (arrives in Phase 4)", errNotImplemented)
		},
	}
}
