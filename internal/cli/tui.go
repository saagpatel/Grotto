package cli

import (
	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/tui"
)

// newTUICmd builds `grotto tui` — launch the interactive Bubble Tea trace
// browser (run list → waterfall → span inspector) over the local store.
func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive trace browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			return tui.Run(ctx, st)
		},
	}
}
