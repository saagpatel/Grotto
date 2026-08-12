package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/saagpatel/grotto/internal/compaction"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/otlp"
	"github.com/saagpatel/grotto/internal/store"
)

const maxOTLPFixtureBytes = 16 << 20

// newCompactionCmd builds `grotto compaction`: render one stored trace, or
// import one synthetic OTLP/JSON export request in memory with --otlp-json.
func newCompactionCmd() *cobra.Command {
	var asJSON bool
	var otlpJSON string
	cmd := &cobra.Command{
		Use:   "compaction <trace-id>",
		Short: "Show compaction boundaries and GenAI response-chain continuity",
		Long: "Render a content-free Compaction X-Ray from a local SQLite trace. " +
			"Use --otlp-json to import one synthetic OTLP export request in memory; " +
			"the command never calls a model or makes an outbound network request.",
		Args: func(cmd *cobra.Command, args []string) error {
			if otlpJSON != "" {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var trace model.Trace
			if otlpJSON != "" {
				var err error
				trace, err = loadOTLPJSON(otlpJSON)
				if err != nil {
					return err
				}
				// Direct fixture imports bypass InsertTrace, so apply the same
				// ingest redaction before any normalization or rendering.
				trace, err = store.Redact(trace)
				if err != nil {
					return fmt.Errorf("redact OTLP fixture: %w", err)
				}
			} else {
				st, err := openStore(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = st.Close() }()
				trace, err = st.GetTrace(ctx, args[0])
				if err != nil {
					return fmt.Errorf("compaction: %w", err)
				}
			}

			report := compaction.Analyze(trace)
			if asJSON {
				return compaction.WriteJSON(cmd.OutOrStdout(), report)
			}
			return compaction.WriteText(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the versioned machine-readable report")
	cmd.Flags().StringVar(&otlpJSON, "otlp-json", "", "import one synthetic OTLP/JSON export request from a local file")
	return cmd
}

func loadOTLPJSON(path string) (model.Trace, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.Trace{}, fmt.Errorf("open OTLP JSON %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxOTLPFixtureBytes+1))
	if err != nil {
		return model.Trace{}, fmt.Errorf("read OTLP JSON %q: %w", path, err)
	}
	if len(data) > maxOTLPFixtureBytes {
		return model.Trace{}, fmt.Errorf("OTLP JSON %q exceeds %d bytes", path, maxOTLPFixtureBytes)
	}
	var request coltracepb.ExportTraceServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, &request); err != nil {
		return model.Trace{}, fmt.Errorf("decode OTLP JSON %q: %w", path, err)
	}
	traces := otlp.MapExportRequest(&request)
	if len(traces) != 1 {
		return model.Trace{}, fmt.Errorf("OTLP JSON %q contains %d traces; expected exactly one", path, len(traces))
	}
	return traces[0], nil
}
