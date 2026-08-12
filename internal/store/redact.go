package store

import (
	"fmt"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/redaction"
)

// Redact applies the same embedded Policy V1 evaluator used by
// `grotto redact-preview`. It returns a deep copy and never mutates t.
func Redact(t model.Trace) (model.Trace, error) {
	evaluator, err := redaction.DefaultEvaluator()
	if err != nil {
		return model.Trace{}, fmt.Errorf("load default redaction policy: %w", err)
	}
	result, err := evaluator.Evaluate(t, redaction.Options{
		SourceKind: "ingest",
		SourceRef:  "InsertTrace",
	})
	if err != nil {
		return model.Trace{}, fmt.Errorf("evaluate redaction policy: %w", err)
	}
	return result.Trace, nil
}
