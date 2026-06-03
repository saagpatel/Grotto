package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/collect"
)

// newMarkCmd builds `grotto mark <name>` — emit a demarcation point to the active
// `grotto run`. Each mark opens a child span that ends at the next mark.
func newMarkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mark <name>",
		Short: "Emit a span mark to the active grotto run",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := collect.Emit(args[0]); err != nil {
				return fmt.Errorf("mark: %w", err)
			}
			return nil
		},
	}
}
