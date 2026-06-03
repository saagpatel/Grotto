package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/otlp"
)

// newServeCmd builds `grotto serve` — run the loopback OTLP receiver (gRPC :4317
// + HTTP :4318), storing exported traces into the same database as marks.
func newServeCmd() *cobra.Command {
	cfg := otlp.Config{
		GRPCAddr: "127.0.0.1:4317",
		HTTPAddr: "127.0.0.1:4318",
	}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local OTLP receiver (gRPC :4317 + HTTP :4318)",
		Long: "Run a loopback OpenTelemetry receiver that accepts trace exports " +
			"over gRPC and HTTP and stores them alongside marks. Ctrl-C stops it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			st, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			cfg.Out = cmd.OutOrStdout()
			cfg.ErrOut = cmd.ErrOrStderr()
			if err := otlp.Serve(ctx, st, cfg); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.GRPCAddr, "grpc-addr", cfg.GRPCAddr, "loopback gRPC OTLP bind address")
	cmd.Flags().StringVar(&cfg.HTTPAddr, "http-addr", cfg.HTTPAddr, "loopback HTTP OTLP bind address")
	return cmd
}
