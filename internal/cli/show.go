package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/render"
)

// newShowCmd builds `grotto show <trace-id>` — print a stored trace as a static
// waterfall, or as JSON with --json.
func newShowCmd() *cobra.Command {
	var asJSON bool
	var limit int
	cmd := &cobra.Command{
		Use:   "show <trace-id>",
		Short: "Print a static waterfall for a stored trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			tr, err := st.GetTrace(ctx, args[0])
			if err != nil {
				return fmt.Errorf("show: %w", err)
			}
			if asJSON {
				return render.WriteJSON(cmd.OutOrStdout(), tr)
			}
			return render.WriteWaterfall(cmd.OutOrStdout(), tr, limit)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the trace as JSON instead of a waterfall")
	cmd.Flags().IntVar(&limit, "limit", render.DefaultMaxRows,
		"max rows per parent before the long tail collapses into a bucket (0 shows all)")
	return cmd
}
