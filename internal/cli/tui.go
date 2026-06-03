package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newTUICmd builds `grotto tui` — launch the interactive Bubble Tea waterfall
// browser. Behavior arrives in Phase 3.
func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive trace browser",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("tui: %w (arrives in Phase 3)", errNotImplemented)
		},
	}
}
