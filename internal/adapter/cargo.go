// Package adapter turns build-tool-native timing output into Grotto spans, so a
// build phase that owns its own compile loop (and is therefore opaque to `grotto
// mark`) becomes a per-unit waterfall. The cargo adapter ingests the UNIT_DATA
// array embedded in `cargo build --timings` HTML reports.
//
// This file is the v1.4 parser spike: extracting and decoding UNIT_DATA, and
// mapping units onto Grotto's OTel span model. See V1.4-CARGO-ADAPTER-DESIGN.md.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// unitDataMarker precedes the JSON array of compilation units in a cargo
// --timings HTML report (`const UNIT_DATA = [ ... ];`).
const unitDataMarker = "const UNIT_DATA = "

// unit is one compilation unit from a cargo --timings report: one crate built in
// one mode. start and duration are seconds relative to the build's internal
// start, at 2-decimal (10ms) precision. Fields beyond these (sections,
// unblocked_units) are ignored here and reserved for v1.5 sub-nesting and
// critical-path work.
type unit struct {
	Index    int     `json:"i"`
	Name     string  `json:"name"`
	Version  string  `json:"version"`
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
}

// parseUnits extracts the UNIT_DATA array embedded in a cargo --timings HTML
// report and decodes it. The array is located by its marker and sliced to the
// matching close bracket with a string-aware scan, so brackets nested in the data
// (sections, unblocked_units) or inside string values cannot truncate it early.
func parseUnits(html []byte) ([]unit, error) {
	i := bytes.Index(html, []byte(unitDataMarker))
	if i < 0 {
		return nil, fmt.Errorf("UNIT_DATA marker not found")
	}
	rest := i + len(unitDataMarker)
	open := bytes.IndexByte(html[rest:], '[')
	if open < 0 {
		return nil, fmt.Errorf("UNIT_DATA opening bracket not found")
	}
	open += rest

	end, err := matchBracket(html, open)
	if err != nil {
		return nil, err
	}

	var units []unit
	if err := json.Unmarshal(html[open:end+1], &units); err != nil {
		return nil, fmt.Errorf("decode UNIT_DATA: %w", err)
	}
	return units, nil
}

// matchBracket returns the index of the ']' that closes the '[' at open, tracking
// nesting depth while skipping JSON string literals (and their escapes) so that a
// bracket inside a string value is never mistaken for structure.
func matchBracket(b []byte, open int) (int, error) {
	depth := 0
	inStr := false
	esc := false
	for i := open; i < len(b); i++ {
		c := b[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated UNIT_DATA array")
}

// unitsToSpans maps cargo compilation units to OTel child spans parented under
// rootID, anchoring cargo's build-relative seconds onto Grotto's absolute
// startNs. Concurrent units yield overlapping [Started, Ended] intervals, which
// the render layer already tolerates. newSpanID supplies fresh span IDs (injected
// so callers control ID generation and tests stay deterministic).
func unitsToSpans(units []unit, rootID, traceID string, startNs int64, newSpanID func() string) []model.Span {
	spans := make([]model.Span, 0, len(units))
	for _, u := range units {
		started := startNs + int64(u.Start*1e9)
		ended := started + int64(u.Duration*1e9)
		spans = append(spans, model.Span{
			SpanID:       newSpanID(),
			TraceID:      traceID,
			ParentSpanID: rootID,
			Name:         fmt.Sprintf("%s v%s", u.Name, u.Version),
			Kind:         model.KindInternal,
			Status:       model.StatusOk,
			StartedNs:    started,
			EndedNs:      ended,
			DurationNs:   ended - started,
		})
	}
	return spans
}

// cargoAdapter implements Adapter for `cargo build` and `cargo test` runs.
// It injects --timings into the cargo invocation and parses the resulting HTML
// report to produce per-crate child spans. Scoped to cargo subcommands that
// accept --timings (build, test); clippy, check, and custom runners may not
// support it — the benign (nil, nil) return from ParseSpans handles those cases.
type cargoAdapter struct{}

// Name returns "cargo", the registry key and Trace.Source value for runs that
// use this adapter.
func (cargoAdapter) Name() string { return "cargo" }

// timingsFlag is the stable-cargo flag that activates the HTML timing report.
const timingsFlag = "--timings"

// PrepareArgv appends --timings to argv if it is not already present, ensuring
// the flag is injected exactly once. Idempotent: if the user already passed
// --timings or --timings=<format>, no second copy is added.
func (cargoAdapter) PrepareArgv(argv []string) []string {
	for _, arg := range argv {
		// Both bare --timings and --timings=html are accepted by cargo;
		// treat any argument that equals or begins with "--timings" as already
		// present so we don't inject a conflicting duplicate.
		if arg == timingsFlag || strings.HasPrefix(arg, timingsFlag+"=") {
			return argv
		}
	}
	return append(argv, timingsFlag)
}

// timingReportPrefix is the prefix of the cargo stderr line that announces
// where the --timings report was written (e.g. "Timing report saved to /…/cargo-timing-….html").
const timingReportPrefix = "Timing report saved to "

// ParseSpans scans bc.Stderr for the cargo timing report path, reads and parses
// the HTML file, and returns one child span per compilation unit parented under
// bc.RootID. Returns (nil, nil) when no timing report is announced — this covers
// both failed builds that emitted no report and cargo subcommands that don't
// support --timings — so the run degrades gracefully to the opaque root span
// rather than failing.
func (cargoAdapter) ParseSpans(ctx context.Context, bc BuildContext) ([]model.Span, error) {
	// Scan stderr line by line for the report-path announcement. Cargo writes
	// exactly one such line; we take the first match.
	path := findTimingReportPath(bc.Stderr)
	if path == "" {
		// No report announced — benign (failed build, unsupported subcommand).
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// Report announced but unreadable (e.g. build aborted before flush);
		// degrade to root-only rather than erroring the whole run.
		return nil, nil
	}

	units, err := parseUnits(data)
	if err != nil {
		return nil, fmt.Errorf("cargo adapter: parse timing report %q: %w", path, err)
	}

	return unitsToSpans(units, bc.RootID, bc.TraceID, bc.StartNs, bc.NewSpanID), nil
}

// findTimingReportPath searches the captured stderr bytes line by line for the
// cargo announcement "Timing report saved to <path>" and returns the path.
// Returns "" when no such line is found.
func findTimingReportPath(stderr []byte) string {
	for _, line := range strings.Split(string(stderr), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, timingReportPrefix) {
			return strings.TrimPrefix(line, timingReportPrefix)
		}
	}
	return ""
}
