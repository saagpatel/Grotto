package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/collect"
)

// newRunCmd builds `grotto run -- <command> [args...]` — execute a command and
// capture the grotto marks it emits into a single trace.
func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command and capture grotto marks into a trace",
		Long: "Run a command, listening for `grotto mark` calls emitted from inside " +
			"it, and store the result as one trace rooted at the command.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			id, err := collect.Run(ctx, st, args)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "stored trace %s\n", id); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
			return nil
		},
	}
}
