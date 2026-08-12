package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/ledger"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/render"
)

// newShowCmd builds `grotto show <trace-id>` — print a stored trace as a static
// waterfall, or as JSON with --json.
func newShowCmd() *cobra.Command {
	var asJSON bool
	var limit int
	var criticalPath bool
	var sections bool
	var asLedger bool
	var asLedgerJSON bool
	var ledgerRates string
	cmd := &cobra.Command{
		Use:   "show <trace-id>",
		Short: "Print a static waterfall for a stored trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ledgerRates != "" && !asLedger && !asLedgerJSON {
				return fmt.Errorf("show: --ledger-rates requires --ledger or --ledger-json")
			}
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
			if asLedger || asLedgerJSON {
				report := ledger.Build(tr)
				if ledgerRates != "" {
					book, loadErr := ledger.LoadRates(ledgerRates)
					if loadErr != nil {
						return fmt.Errorf("show: %w", loadErr)
					}
					ledger.ApplyRates(&report, book)
				}
				if asLedgerJSON {
					return ledger.WriteJSON(cmd.OutOrStdout(), report)
				}
				return ledger.WriteText(cmd.OutOrStdout(), report)
			}
			if criticalPath {
				return render.WriteCriticalPath(cmd.OutOrStdout(), tr)
			}
			// Cargo per-crate frontend/codegen sub-phases are stored but hidden by
			// default so the waterfall stays uncluttered; --sections reveals them.
			if !sections {
				tr.Spans = withoutSections(tr.Spans)
			}
			return render.WriteWaterfall(cmd.OutOrStdout(), tr, limit)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the trace as JSON instead of a waterfall")
	cmd.Flags().IntVar(&limit, "limit", render.DefaultMaxRows,
		"max rows per parent before the long tail collapses into a bucket (0 shows all)")
	cmd.Flags().BoolVar(&criticalPath, "critical-path", false,
		"show the longest dependency chain (build floor) instead of the waterfall; cargo-adapter traces only")
	cmd.Flags().BoolVar(&sections, "sections", false,
		"show cargo per-crate frontend/codegen sub-phases nested under each crate")
	cmd.Flags().BoolVar(&asLedger, "ledger", false,
		"show a causal token, cache, tool, and context ledger")
	cmd.Flags().BoolVar(&asLedgerJSON, "ledger-json", false,
		"output the versioned cache/context ledger report as JSON")
	cmd.Flags().StringVar(&ledgerRates, "ledger-rates", "",
		"local versioned user-supplied rate file for optional estimates")
	// These select different renderings; combining them would silently pick one.
	cmd.MarkFlagsMutuallyExclusive("json", "critical-path", "ledger", "ledger-json")
	cmd.MarkFlagsMutuallyExclusive("critical-path", "sections")
	cmd.MarkFlagsMutuallyExclusive("ledger", "sections")
	cmd.MarkFlagsMutuallyExclusive("ledger-json", "sections")
	return cmd
}

// withoutSections drops cargo sub-phase spans (frontend/codegen) so the default
// waterfall is unchanged for cargo traces; they remain in the store and in
// --json output. Non-cargo traces have no such spans and pass through untouched.
func withoutSections(spans []model.Span) []model.Span {
	out := make([]model.Span, 0, len(spans))
	for _, s := range spans {
		if hasAttr(s, adapter.AttrSection) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func hasAttr(s model.Span, key string) bool {
	for _, a := range s.Attributes {
		if a.Key == key {
			return true
		}
	}
	return false
}
