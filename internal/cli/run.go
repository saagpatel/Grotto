package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/collect"
)

// newRunCmd builds `grotto run -- <command> [args...]` — execute a command and
// capture the grotto marks it emits into a single trace. An optional
// --adapter flag activates a build-tool adapter (e.g. "cargo") that injects
// tool-specific timing flags and produces per-unit child spans from the
// build's own timing report, turning an opaque build bar into a crate-level
// waterfall without any source changes.
func newRunCmd() *cobra.Command {
	var adapterName string

	cmd := &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command and capture grotto marks into a trace",
		Long: "Run a command, listening for `grotto mark` calls emitted from inside " +
			"it, and store the result as one trace rooted at the command.\n\n" +
			"Use --adapter=cargo to capture per-crate timing from a `cargo build`/`cargo " +
			"test`, or --adapter=go-test to capture per-package/per-test timing from a " +
			"`go test` run — without modifying your source.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			// Resolve the adapter: empty string means "no adapter" (nil passed to
			// collect.Run preserves today's behavior exactly). An unrecognised name
			// is a user error surfaced immediately so the run does not start.
			var ad adapter.Adapter
			if adapterName != "" {
				var ok bool
				ad, ok = adapter.Lookup(adapterName)
				if !ok {
					return fmt.Errorf("unknown adapter %q (available: %s)", adapterName, strings.Join(adapter.Names(), ", "))
				}
			}

			id, err := collect.Run(ctx, st, args, ad)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "stored trace %s\n", id); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&adapterName, "adapter", "",
		"build-tool adapter to emit per-unit spans (cargo, go-test)")

	return cmd
}
