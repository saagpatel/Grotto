package ledger

import "fmt"

// ValidateReport enforces the executable invariants paired with the published
// JSON Schema. It is intentionally dependency-free so validation cannot add a
// second runtime or network surface.
func ValidateReport(report Report) error {
	if report.Schema != ReportSchema {
		return fmt.Errorf("schema = %q, want %q", report.Schema, ReportSchema)
	}
	if report.TraceID == "" {
		return fmt.Errorf("trace_id is required")
	}
	if report.Summary.SpanCount != len(report.Rows) {
		return fmt.Errorf("summary span_count=%d does not match rows=%d", report.Summary.SpanCount, len(report.Rows))
	}
	for i := range report.Rows {
		if report.Rows[i].SpanID == "" {
			return fmt.Errorf("rows[%d].span_id is required", i)
		}
		if i > 0 && rowLess(report.Rows[i], report.Rows[i-1]) {
			return fmt.Errorf("rows are not deterministically ordered at index %d", i)
		}
		if err := validateUsage(report.Rows[i].Observed); err != nil {
			return fmt.Errorf("rows[%d].observed: %w", i, err)
		}
		if err := validateUsage(report.Rows[i].Contribution); err != nil {
			return fmt.Errorf("rows[%d].contribution: %w", i, err)
		}
	}
	if err := validateUsage(report.Summary.Usage); err != nil {
		return fmt.Errorf("summary.usage: %w", err)
	}
	if report.Summary.Context.Status == "known" {
		if report.Summary.Context.Ratio == nil || !isKnown(report.Summary.Context.Limit) || !isKnown(report.Summary.Context.Used) {
			return fmt.Errorf("known context pressure requires ratio, limit, and used")
		}
	} else if report.Summary.Context.Ratio != nil {
		return fmt.Errorf("unknown context pressure requires null ratio")
	}
	if estimate := report.Summary.Estimate; estimate != nil {
		if estimate.Provenance.Schema != RateSchema || estimate.Provenance.SHA256 == "" {
			return fmt.Errorf("estimate requires rate schema and digest provenance")
		}
		if estimate.Status == "known" && estimate.Amount == nil {
			return fmt.Errorf("known estimate requires amount")
		}
		if estimate.Status == "unknown" && estimate.Amount != nil {
			return fmt.Errorf("unknown estimate requires null amount")
		}
	}
	return nil
}

func validateUsage(usage Usage) error {
	counts := map[string]Count{
		"input": usage.Input, "output": usage.Output, "cache_read": usage.CacheRead,
		"cache_write": usage.CacheWrite, "reasoning": usage.Reasoning,
		"tool_input": usage.ToolInput, "total": usage.Total,
	}
	for name, count := range counts {
		if count.Status != "known" && count.Status != "unknown" {
			return fmt.Errorf("%s has invalid status %q", name, count.Status)
		}
		if count.Status == "known" {
			if count.Value == nil || *count.Value < 0 {
				return fmt.Errorf("%s known value must be non-negative", name)
			}
		} else if count.Value != nil {
			return fmt.Errorf("%s unknown value must be null", name)
		}
	}
	if isKnown(usage.Input) && isKnown(usage.Output) && isKnown(usage.Total) {
		want, overflow := checkedAdd(*usage.Input.Value, *usage.Output.Value)
		if overflow || *usage.Total.Value != want {
			return fmt.Errorf("total does not equal input + output")
		}
	}
	return nil
}

func rowLess(left, right Row) bool {
	if left.StartedNs != right.StartedNs {
		return left.StartedNs < right.StartedNs
	}
	if left.EndedNs != right.EndedNs {
		return left.EndedNs < right.EndedNs
	}
	return left.SpanID < right.SpanID
}
