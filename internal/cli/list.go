package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/render"
)

// newListCmd builds `grotto list` — print the most recent traces newest-first as
// a table (trace id, span count, duration, source, age, label).
func newListCmd() *cobra.Command {
	const limit = 50
	return &cobra.Command{
		Use:   "list",
		Short: "List recent traces newest-first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			rows, err := st.RecentTraces(ctx, limit)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			return render.WriteTraceList(cmd.OutOrStdout(), rows, time.Now())
		},
	}
}
