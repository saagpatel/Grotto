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
	"regexp"
	"strconv"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// unitDataMarker precedes the JSON array of compilation units in a cargo
// --timings HTML report (`const UNIT_DATA = [ ... ];`).
const unitDataMarker = "const UNIT_DATA = "

// unit is one compilation unit from a cargo --timings report: one crate built in
// one mode. start and duration are seconds relative to the build's internal
// start, at 2-decimal (10ms) precision.
type unit struct {
	Index    int     `json:"i"`
	Name     string  `json:"name"`
	Version  string  `json:"version"`
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	// Target distinguishes units that share a name+version: "" is the normal
	// library/binary compile, " build-script" is compiling a build.rs, and
	// " build-script (run)" is executing it. A crate that has a build script and
	// is also used by a host-side proc-macro can appear three times — Target is
	// what tells them apart in the waterfall.
	Target string `json:"target"`
	// UnblockedUnits lists the indices of units that this unit unblocks when it
	// finishes compiling — the forward edges of the build's dependency DAG. They
	// are stamped onto each span as attributes so the critical-path analysis can
	// reconstruct the graph from a stored trace.
	UnblockedUnits []int `json:"unblocked_units"`
	// Sections are the unit's compile sub-phases (frontend = parse/typecheck/
	// borrow-check, codegen = LLVM codegen/optimization), with times relative to
	// the unit's own start. Null for units that do not split (build scripts,
	// their runs, some proc-macros and binaries).
	Sections []section `json:"sections"`
}

// AttrSection marks a span as a cargo compile sub-phase (frontend/codegen). The
// `grotto show` waterfall hides these by default and reveals them with
// --sections; critical-path and rollup ignore them via this marker.
const AttrSection = "cargo.section"

// section is one compile sub-phase of a unit. Cargo encodes it as a 2-tuple
// [name, {start,end}], with start/end in seconds relative to the unit's start.
type section struct {
	Name  string
	Start float64
	End   float64
}

// UnmarshalJSON decodes cargo's ["<name>", {"start":..,"end":..}] tuple shape.
func (s *section) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return fmt.Errorf("section: want [name, {start,end}], got %d elements", len(raw))
	}
	if err := json.Unmarshal(raw[0], &s.Name); err != nil {
		return fmt.Errorf("section name: %w", err)
	}
	var span struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	}
	if err := json.Unmarshal(raw[1], &span); err != nil {
		return fmt.Errorf("section span: %w", err)
	}
	s.Start, s.End = span.Start, span.End
	return nil
}

// displayName is the span name for a unit: "<crate> v<version>", suffixed with
// the cargo target label (e.g. " (build-script)") when it is not the plain
// library compile, so the three units of a single crate read as distinct rows
// instead of identical duplicates.
func (u unit) displayName() string {
	name := fmt.Sprintf("%s v%s", u.Name, u.Version)
	if t := strings.TrimSpace(u.Target); t != "" {
		name += " (" + t + ")"
	}
	return name
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
		// Clamp defensively, mirroring assembleTrace: a unit can never start
		// before the command's own start, and a span cannot end before it begins.
		// Real cargo output satisfies both, but a corrupt report must not produce
		// a span that precedes the root or has a negative duration (which would
		// then poison gap math and the rollup bucket's summed time).
		if started < startNs {
			started = startNs
		}
		ended := started + int64(u.Duration*1e9)
		if ended < started {
			ended = started
		}
		crateID := newSpanID()
		spans = append(spans, model.Span{
			SpanID:       crateID,
			TraceID:      traceID,
			ParentSpanID: rootID,
			Name:         u.displayName(),
			Kind:         model.KindInternal,
			Status:       model.StatusOk,
			StartedNs:    started,
			EndedNs:      ended,
			DurationNs:   ended - started,
			Attributes:   u.dagAttrs(),
		})

		// Sub-phase children (frontend/codegen), parented to this crate. Their
		// cargo times are relative to the unit's start; clamp inside the crate's
		// interval so a corrupt report cannot push a sub-phase outside its parent.
		for _, sec := range u.Sections {
			ss := started + int64(sec.Start*1e9)
			se := started + int64(sec.End*1e9)
			if ss < started {
				ss = started
			}
			if se > ended {
				se = ended
			}
			if se < ss {
				se = ss
			}
			spans = append(spans, model.Span{
				SpanID:       newSpanID(),
				TraceID:      traceID,
				ParentSpanID: crateID,
				Name:         sec.Name,
				Kind:         model.KindInternal,
				Status:       model.StatusOk,
				StartedNs:    ss,
				EndedNs:      se,
				DurationNs:   se - ss,
				Attributes:   []model.Attribute{{Key: AttrSection, ValueType: "str", Value: sec.Name}},
			})
		}
	}
	return spans
}

// dagAttrs stamps the unit's place in the build dependency DAG onto its span:
// cargo.unit is this unit's index, and cargo.unblocks (present only when there
// are edges) is the comma-separated list of unit indices this one unblocks. The
// critical-path analysis reconstructs the graph from these attributes, so the
// edges survive the trip through SQLite as ordinary OTel span attributes.
func (u unit) dagAttrs() []model.Attribute {
	attrs := []model.Attribute{
		{Key: "cargo.unit", ValueType: "int", Value: strconv.Itoa(u.Index)},
	}
	if len(u.UnblockedUnits) > 0 {
		ids := make([]string, len(u.UnblockedUnits))
		for i, idx := range u.UnblockedUnits {
			ids[i] = strconv.Itoa(idx)
		}
		attrs = append(attrs, model.Attribute{
			Key: "cargo.unblocks", ValueType: "str", Value: strings.Join(ids, ","),
		})
	}
	return attrs
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

// ansiSGR matches ANSI SGR (color/style) escape sequences. Cargo emits them on
// stderr when color is forced (e.g. CLICOLOR_FORCE=1 in CI), which would
// otherwise hide the timing-report marker from a plain text match.
var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// findTimingReportPath searches the captured stderr for cargo's announcement
// "Timing report saved to <path>" and returns the path, or "" when absent. It
// strips ANSI styling and matches the marker as a substring (not a strict
// prefix) so a color-forced or otherwise-decorated line still resolves.
func findTimingReportPath(stderr []byte) string {
	for _, line := range strings.Split(string(stderr), "\n") {
		line = ansiSGR.ReplaceAllString(line, "")
		if i := strings.Index(line, timingReportPrefix); i >= 0 {
			return strings.TrimSpace(line[i+len(timingReportPrefix):])
		}
	}
	return ""
}
