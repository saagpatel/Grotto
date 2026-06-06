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
	var sortMode string
	cmd := &cobra.Command{
		Use:   "diff <trace-a> <trace-b>",
		Short: "Compare two traces span-by-span",
		Long: "Compare two stored traces span-by-span. Spans pair by name and depth; " +
			"matched spans show A → B ±delta. With --sort=delta the biggest movers " +
			"come first — e.g. the crates a warm cargo cache saved the most time on.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sortMode != "structural" && sortMode != "delta" {
				return fmt.Errorf("invalid --sort %q (want structural or delta)", sortMode)
			}
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

			deltas := render.Diff(a, b)
			if sortMode == "delta" {
				render.SortByImpact(deltas)
			}
			return render.WriteDiff(cmd.OutOrStdout(), a, b, deltas)
		},
	}
	cmd.Flags().StringVar(&sortMode, "sort", "structural",
		"row order: structural (tree pre-order) or delta (biggest change first)")
	return cmd
}
