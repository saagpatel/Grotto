package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/render"
)

// newDiffCmd builds `grotto diff <trace-a> <trace-b>` — load two stored traces
// and print per-span duration deltas (matched spans show A → B ±delta; spans
// only in one trace are marked + / −).
func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <trace-a> <trace-b>",
		Short: "Compare two traces span-by-span",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			a, err := st.GetTrace(ctx, args[0])
			if err != nil {
				return fmt.Errorf("diff: load %q: %w", args[0], err)
			}
			b, err := st.GetTrace(ctx, args[1])
			if err != nil {
				return fmt.Errorf("diff: load %q: %w", args[1], err)
			}
			return render.WriteDiff(cmd.OutOrStdout(), a, b, render.Diff(a, b))
		},
	}
}
