package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/collect"
)

// newMarkCmd builds `grotto mark <name>` — emit a demarcation point to the active
// `grotto run`. Each mark opens a child span that ends at the next mark; with
// --child the span nests one level under the most recent non-child mark.
func newMarkCmd() *cobra.Command {
	var child bool
	cmd := &cobra.Command{
		Use:   "mark <name>",
		Short: "Emit a span mark to the active grotto run",
		Long: "Emit a span mark to the active grotto run.\n\n" +
			"Each mark opens a child span that ends at the next mark. Pass --child to\n" +
			"nest this mark one level under the most recent non-child mark, subdividing\n" +
			"that section (e.g. break a 'compile' phase into 'gcc' and 'ld' sub-steps).",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := collect.Emit(args[0], child); err != nil {
				return fmt.Errorf("mark: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&child, "child", false,
		"nest under the most recent non-child mark (one level deep)")
	return cmd
}
