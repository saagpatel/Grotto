package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newServeCmd builds `grotto serve` — run the loopback OTLP receiver
// (gRPC :4317 + HTTP :4318). Behavior arrives in Phase 2.
func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the local OTLP receiver (gRPC :4317 + HTTP :4318)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("serve: %w (arrives in Phase 2)", errNotImplemented)
		},
	}
}
