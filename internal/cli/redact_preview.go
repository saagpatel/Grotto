package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/redaction"
	"github.com/saagpatel/grotto/internal/store"
)

const maxImportedTraceBytes = 16 * 1024 * 1024

// newRedactPreviewCmd builds the non-mutating redaction disclosure preview.
// Stored traces use a locking read-only SQLite connection; imported traces are
// read once and never rewritten. There is deliberately no reveal flag.
func newRedactPreviewCmd() *cobra.Command {
	var (
		filePath   string
		policyPath string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "redact-preview [trace-id]",
		Short: "Preview the safe disclosure plan for a stored or imported trace",
		Long: "Evaluate the same versioned redaction policy used at ingest and print " +
			"field paths, rules, actions, lengths, and safe previews without changing the source.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (filePath == "") == (len(args) == 0) {
				return fmt.Errorf("provide exactly one source: a trace-id or --file")
			}
			evaluator, err := loadRedactionEvaluator(policyPath)
			if err != nil {
				return err
			}

			var (
				trace model.Trace
				opts  redaction.Options
			)
			if filePath != "" {
				trace, err = readTraceFile(filePath)
				opts = redaction.Options{SourceKind: "file", SourceRef: "imported-trace"}
			} else {
				trace, err = readStoredTrace(cmd, args[0])
				opts = redaction.Options{SourceKind: "sqlite", SourceRef: "stored-trace"}
			}
			if err != nil {
				return err
			}
			result, err := evaluator.Evaluate(trace, opts)
			if err != nil {
				return fmt.Errorf("redact-preview: %w", err)
			}
			if asJSON {
				return redaction.WriteJSON(cmd.OutOrStdout(), result.Report)
			}
			return redaction.WriteText(cmd.OutOrStdout(), result.Report)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "read a Grotto trace JSON file instead of SQLite")
	cmd.Flags().StringVar(&policyPath, "policy", "", "use a Policy V1 JSON file instead of the embedded safe default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the versioned preview report as JSON")
	return cmd
}

func loadRedactionEvaluator(policyPath string) (*redaction.Evaluator, error) {
	if policyPath == "" {
		evaluator, err := redaction.DefaultEvaluator()
		if err != nil {
			return nil, fmt.Errorf("load embedded redaction policy: %w", err)
		}
		return evaluator, nil
	}
	f, err := os.Open(policyPath)
	if err != nil {
		return nil, fmt.Errorf("open redaction policy %q: %w", policyPath, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat redaction policy %q: %w", policyPath, err)
	}
	if info.Size() > 1024*1024 {
		return nil, fmt.Errorf("redaction policy %q exceeds 1048576 bytes", policyPath)
	}
	evaluator, err := redaction.LoadPolicy(f)
	if err != nil {
		return nil, fmt.Errorf("load redaction policy %q: %w", policyPath, err)
	}
	return evaluator, nil
}

func readTraceFile(path string) (model.Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Trace{}, fmt.Errorf("open trace file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return model.Trace{}, fmt.Errorf("stat trace file %q: %w", path, err)
	}
	if info.Size() > maxImportedTraceBytes {
		return model.Trace{}, fmt.Errorf("trace file %q exceeds %d bytes", path, maxImportedTraceBytes)
	}
	dec := json.NewDecoder(io.LimitReader(f, maxImportedTraceBytes+1))
	dec.DisallowUnknownFields()
	var trace model.Trace
	if err := dec.Decode(&trace); err != nil {
		return model.Trace{}, fmt.Errorf("decode trace file %q: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return model.Trace{}, fmt.Errorf("decode trace file %q: multiple JSON values", path)
		}
		return model.Trace{}, fmt.Errorf("decode trace file %q trailer: %w", path, err)
	}
	return trace, nil
}

func readStoredTrace(cmd *cobra.Command, traceID string) (model.Trace, error) {
	path, err := store.DefaultDBPath()
	if err != nil {
		return model.Trace{}, err
	}
	st, err := store.OpenReadOnly(cmd.Context(), path)
	if err != nil {
		return model.Trace{}, fmt.Errorf("redact-preview: %w", err)
	}
	defer func() { _ = st.Close() }()
	trace, err := st.GetTrace(cmd.Context(), traceID)
	if err != nil {
		return model.Trace{}, fmt.Errorf("redact-preview: get trace %q: %w", traceID, err)
	}
	return trace, nil
}
