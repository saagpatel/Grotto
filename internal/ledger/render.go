package ledger

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteJSON writes the versioned report with stable indentation and field order.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode cache/context ledger: %w", err)
	}
	return nil
}

// WriteText writes a compact deterministic causal ledger.
func WriteText(w io.Writer, report Report) error {
	var out strings.Builder
	fmt.Fprintf(&out, "cache/context ledger v1  trace %s  %s\n", report.TraceID, report.RunLabel)
	fmt.Fprintf(&out, "summary  input=%s output=%s total=%s cache-read=%s cache-write=%s reasoning=%s tool-input=%s tool-calls=%d retries=%d\n",
		formatCount(report.Summary.Usage.Input), formatCount(report.Summary.Usage.Output),
		formatCount(report.Summary.Usage.Total), formatCount(report.Summary.Usage.CacheRead),
		formatCount(report.Summary.Usage.CacheWrite), formatCount(report.Summary.Usage.Reasoning),
		formatCount(report.Summary.Usage.ToolInput), report.Summary.ToolCalls, report.Summary.RetryAttempts)
	if report.Summary.Context.Status == "known" {
		fmt.Fprintf(&out, "context  used=%s limit=%s pressure=%.1f%%\n",
			formatCount(report.Summary.Context.Used), formatCount(report.Summary.Context.Limit),
			*report.Summary.Context.Ratio*100)
	} else {
		fmt.Fprintf(&out, "context  UNKNOWN (%s)\n", report.Summary.Context.Reason)
	}
	if estimate := report.Summary.Estimate; estimate != nil {
		if estimate.Status == "known" {
			fmt.Fprintf(&out, "estimate  %s %s  as-of=%s sha256=%s\n",
				*estimate.Amount, estimate.Provenance.Currency, estimate.Provenance.AsOf, estimate.Provenance.SHA256)
		} else {
			fmt.Fprintf(&out, "estimate  UNKNOWN (%s)  as-of=%s sha256=%s\n",
				estimate.Reason, estimate.Provenance.AsOf, estimate.Provenance.SHA256)
		}
	}
	out.WriteString("\ncausal rows\n")
	for _, row := range report.Rows {
		indent := strings.Repeat("  ", row.Depth)
		marker := "-"
		if row.Contributes {
			marker = "+"
		}
		fmt.Fprintf(&out, "%s%s %s %s  mode=%s branch=%s input=%s output=%s cache-r=%s cache-w=%s reasoning=%s tool=%s total=%s\n",
			indent, marker, row.SpanID, row.Name, row.Mode, displayUnknown(row.BranchSpanID),
			formatCount(row.Contribution.Input), formatCount(row.Contribution.Output),
			formatCount(row.Contribution.CacheRead), formatCount(row.Contribution.CacheWrite),
			formatCount(row.Contribution.Reasoning), formatCount(row.Contribution.ToolInput),
			formatCount(row.Contribution.Total))
		for _, source := range rowSources(row) {
			derived := ""
			if source.Derived != "" {
				derived = " (" + source.Derived + ")"
			}
			fmt.Fprintf(&out, "%s    <- %s=%s [%s/%s]%s\n", indent, source.Attribute, source.Value, source.ValueType, source.Family, derived)
		}
	}
	out.WriteString("\nreconciliation\n")
	if len(report.Issues) == 0 {
		out.WriteString("  OK\n")
	} else {
		for _, issue := range report.Issues {
			target := issue.SpanID
			if target == "" {
				target = "trace"
			}
			fmt.Fprintf(&out, "  %s %s: %s\n", issue.Code, target, issue.Message)
		}
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("write cache/context ledger: %w", err)
	}
	return nil
}

func formatCount(count Count) string {
	if !isKnown(count) {
		return "UNKNOWN"
	}
	return fmt.Sprintf("%d", *count.Value)
}

func displayUnknown(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func rowSources(row Row) []Source {
	all := make([]Source, 0)
	counts := []Count{
		row.Observed.Input, row.Observed.Output, row.Observed.CacheRead,
		row.Observed.CacheWrite, row.Observed.Reasoning, row.Observed.ToolInput,
	}
	seen := make(map[string]struct{})
	for _, count := range counts {
		for _, source := range count.Sources {
			key := strings.Join([]string{source.Attribute, source.Value, source.ValueType, source.Family, source.Derived}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			all = append(all, source)
		}
	}
	sortSources(all)
	return all
}

// StableIssueCodes returns sorted unique issue codes for compact assertions.
func StableIssueCodes(report Report) []string {
	seen := make(map[string]struct{})
	for _, issue := range report.Issues {
		seen[issue.Code] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
