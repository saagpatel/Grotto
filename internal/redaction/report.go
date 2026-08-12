package redaction

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/saagpatel/grotto/internal/model"
)

// Options identifies the non-sensitive source boundary evaluated for a report.
type Options struct {
	SourceKind string
	SourceRef  string
}

// PolicyProvenance binds a report to an exact policy and evaluator version.
type PolicyProvenance struct {
	PolicyID         string `json:"policy_id"`
	PolicyVersion    string `json:"policy_version"`
	PolicySHA256     string `json:"policy_sha256"`
	EvaluatorVersion string `json:"evaluator_version"`
}

// ReportSource describes the evaluated source without exposing a filesystem or
// database path.
type ReportSource struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	TraceID string `json:"trace_id"`
}

// Decision is one safe field-level disclosure decision.
type Decision struct {
	Path                string `json:"path"`
	Category            string `json:"category"`
	MatchedRule         string `json:"matched_rule"`
	RuleProvenance      string `json:"rule_provenance"`
	Action              Action `json:"action"`
	Explanation         string `json:"explanation"`
	Precedence          string `json:"precedence"`
	OriginalType        string `json:"original_type"`
	OriginalLengthBytes int    `json:"original_length_bytes"`
	Preview             string `json:"preview,omitempty"`
	Digest              string `json:"digest,omitempty"`
	Status              string `json:"status"`
	UnknownReason       string `json:"unknown_reason,omitempty"`
}

// Summary is a deterministic action and uncertainty denominator.
type Summary struct {
	Fields    int `json:"fields"`
	Retained  int `json:"retained"`
	Masked    int `json:"masked"`
	Hashed    int `json:"hashed"`
	Truncated int `json:"truncated"`
	Dropped   int `json:"dropped"`
	Unknown   int `json:"unknown"`
}

// Report is the versioned raw-content-off preview contract.
type Report struct {
	Schema    string           `json:"schema"`
	Policy    PolicyProvenance `json:"policy"`
	Source    ReportSource     `json:"source"`
	Summary   Summary          `json:"summary"`
	Decisions []Decision       `json:"decisions"`
}

// Result couples the applied copy with the exact decisions used to create it.
type Result struct {
	Trace  model.Trace
	Report Report
}

// WriteJSON writes the deterministic report contract.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode redaction preview: %w", err)
	}
	return nil
}

// WriteText writes a compact safe-default operator view.
func WriteText(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "redaction preview  policy=%s@%s  fields=%d  unknown=%d\n",
		report.Policy.PolicyID, report.Policy.PolicyVersion, report.Summary.Fields, report.Summary.Unknown); err != nil {
		return fmt.Errorf("write redaction preview header: %w", err)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PATH\tCATEGORY\tRULE\tACTION\tORIGINAL\tPREVIEW/DIGEST"); err != nil {
		return fmt.Errorf("write redaction preview columns: %w", err)
	}
	for _, d := range report.Decisions {
		preview := d.Preview
		if d.Digest != "" {
			preview = d.Digest
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s/%dB\t%s\n",
			d.Path, d.Category, d.MatchedRule, d.Action, d.OriginalType,
			d.OriginalLengthBytes, preview); err != nil {
			return fmt.Errorf("write redaction preview row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush redaction preview: %w", err)
	}
	return nil
}
