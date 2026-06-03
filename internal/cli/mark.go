package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newMarkCmd builds `grotto mark` — emit a span record to the run socket from
// inside an instrumented script. Behavior arrives in Phase 1.
func newMarkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mark <name>",
		Short: "Emit a span mark to the active grotto run",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("mark: %w (arrives in Phase 1)", errNotImplemented)
		},
	}
}
