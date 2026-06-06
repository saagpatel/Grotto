// Package adapter's junit adapter turns a JUnit XML report into Grotto spans, so
// any test runner that emits the (near-universal) JUnit schema becomes a per-suite/
// per-test waterfall. v1.8 targets pytest out of the box: PrepareArgv injects
// --junitxml pointed at grotto's per-run scratch dir, and ParseSpans reads it back.
//
// Unlike cargo (whose --timings report carries absolute start offsets) and go-test
// (absolute event timestamps), JUnit XML records only a per-testcase DURATION — no
// start time. Spans are therefore laid out sequentially within each suite (each test
// begins where the previous ended), which matches a serial pytest run exactly and
// approximates a parallel one (e.g. pytest-xdist): durations are real, the left-to-
// right ordering is synthesized.
package adapter

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// junitReportName is the fixed filename grotto writes the JUnit report to inside the
// per-run scratch dir, so PrepareArgv (which sets --junitxml) and ParseSpans (which
// reads it) agree on the path without threading state through the adapter value.
const junitReportName = "junit.xml"

// junitTestSuites is the multi-suite root (<testsuites>) that wraps one or more
// <testsuite> elements — pytest's default shape.
type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

// junitTestSuite is one <testsuite>: a named group of testcases with a total time.
// It may also appear as the document root (no <testsuites> wrapper), which some
// runners emit; parseJUnit handles both shapes.
type junitTestSuite struct {
	XMLName xml.Name        `xml:"testsuite"`
	Name    string          `xml:"name,attr"`
	Time    float64         `xml:"time,attr"`
	Cases   []junitTestCase `xml:"testcase"`
}

// junitTestCase is one <testcase>. Time is the test's duration in seconds. A child
// <failure>/<error> marks the case failed (error status); <skipped> marks it skipped
// (ok status — a skip is not a failure, matching the go-test adapter's treatment).
type junitTestCase struct {
	Name    string        `xml:"name,attr"`
	Time    float64       `xml:"time,attr"`
	Failure *junitOutcome `xml:"failure"`
	Error   *junitOutcome `xml:"error"`
	Skipped *junitOutcome `xml:"skipped"`
}

// junitOutcome is a failure/error/skipped child element; presence is what matters,
// so only the element pointer is consulted (its body/attrs are ignored).
type junitOutcome struct{}

// junitAdapter implements Adapter for JUnit-XML-emitting runners (pytest in v1.8).
// It is a file-reader adapter like cargo: CapturesStdout is false, the child's
// stdout/stderr pass through to the user, and ParseSpans reads a report file.
type junitAdapter struct{}

// Name returns "junit", the registry key and Trace.Source for junit runs.
func (junitAdapter) Name() string { return "junit" }

// CapturesStdout is false: junit reads an XML file, not the child's stdout, so the
// run's output passes through to the user untouched.
func (junitAdapter) CapturesStdout() bool { return false }

// junitFlag and junitFlagAlt are the two spellings pytest accepts for the report
// path; both are stripped from argv before grotto appends its own, so grotto always
// controls where the report lands (and can read it back from the scratch dir).
const (
	junitFlag    = "--junitxml"
	junitFlagAlt = "--junit-xml"
)

// PrepareArgv points the runner's JUnit output at grotto's scratch dir. Any
// user-supplied --junitxml/--junit-xml (in either "=value" or separate-token form)
// is removed first so grotto's path is authoritative — the adapter must read the
// report back from a location it chose. Idempotent: the stripped, single grotto
// flag is the only --junitxml in the result.
func (junitAdapter) PrepareArgv(argv []string, scratchDir string) []string {
	out := make([]string, 0, len(argv)+1)
	stripped := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == junitFlag || a == junitFlagAlt {
			i++ // also skip the following value token ("--junitxml path" form)
			stripped = true
			continue
		}
		if strings.HasPrefix(a, junitFlag+"=") || strings.HasPrefix(a, junitFlagAlt+"=") {
			stripped = true
			continue
		}
		out = append(out, a)
	}
	if stripped {
		// grotto must own the report path to read it back from its scratch dir, so a
		// user-supplied --junitxml is overridden. Warn rather than silently drop it —
		// the user's report file would otherwise never be written (the scratch dir is
		// removed at run end), which is surprising if they expected a CI artifact.
		fmt.Fprintln(os.Stderr, "grotto: junit adapter: overriding your --junitxml; grotto manages the report path for this run")
	}
	return append(out, junitFlag+"="+filepath.Join(scratchDir, junitReportName))
}

// ParseSpans reads the JUnit report grotto told the runner to write into bc.ScratchDir
// and returns one span per suite with its testcases nested beneath. A missing report
// (the runner crashed before writing it, or never ran) is benign: it returns
// (nil, nil) so the run degrades to the opaque root span. A present-but-malformed
// report is an error (warned by collect, base trace still stored).
func (junitAdapter) ParseSpans(_ context.Context, bc BuildContext) ([]model.Span, error) {
	path := filepath.Join(bc.ScratchDir, junitReportName)
	data, err := os.ReadFile(path)
	if err != nil {
		// No report (crash before flush, or a runner that ignored --junitxml).
		return nil, nil
	}

	suites, err := parseJUnit(data)
	if err != nil {
		return nil, fmt.Errorf("junit adapter: parse report %q: %w", path, err)
	}
	return suitesToSpans(suites, bc.RootID, bc.TraceID, bc.StartNs, bc.EndNs, bc.NewSpanID), nil
}

// parseJUnit decodes a JUnit report whose root is either <testsuites> (a wrapper of
// one or more suites, pytest's default) or a bare <testsuite> (some runners). It
// peeks the root element to dispatch to the right shape — matching on the local name
// so a namespaced root still resolves — and decodes into it, so a malformed report
// surfaces its real parse error rather than a misleading root-mismatch one.
func parseJUnit(data []byte) ([]junitTestSuite, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("no <testsuites>/<testsuite> root element: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue // skip the xml declaration, comments, and whitespace
		}
		switch start.Name.Local {
		case "testsuites":
			var multi junitTestSuites
			if err := dec.DecodeElement(&multi, &start); err != nil {
				return nil, err
			}
			return multi.Suites, nil
		case "testsuite":
			var single junitTestSuite
			if err := dec.DecodeElement(&single, &start); err != nil {
				return nil, err
			}
			return []junitTestSuite{single}, nil
		default:
			return nil, fmt.Errorf("unexpected root element <%s>, want <testsuites> or <testsuite>", start.Name.Local)
		}
	}
}

// suitesToSpans maps suites → testcases onto a span tree parented under rootID.
// JUnit has no absolute times, so suites are laid end-to-end starting at startNs and
// testcases sequentially within their suite; every bound is clamped inside
// [startNs, endNs] so a report whose times overrun the measured run can't produce a
// span outside the root.
func suitesToSpans(suites []junitTestSuite, rootID, traceID string, startNs, endNs int64, newSpanID func() string) []model.Span {
	var spans []model.Span
	cursor := startNs

	for _, suite := range suites {
		suiteStart := clamp(cursor, startNs, endNs)
		suiteID := newSpanID()

		caseCursor := suiteStart
		status := model.StatusOk
		caseSpans := make([]model.Span, 0, len(suite.Cases))
		for _, tc := range suite.Cases {
			cStart := clamp(caseCursor, startNs, endNs)
			cEnd := clamp(caseCursor+secondsToNs(tc.Time), startNs, endNs)
			caseStatus := model.StatusOk
			if tc.Failure != nil || tc.Error != nil {
				caseStatus, status = model.StatusError, model.StatusError
			}
			caseSpans = append(caseSpans, model.Span{
				SpanID:       newSpanID(),
				TraceID:      traceID,
				ParentSpanID: suiteID,
				Name:         tc.Name,
				Kind:         model.KindInternal,
				Status:       caseStatus,
				StartedNs:    cStart,
				EndedNs:      cEnd,
				DurationNs:   cEnd - cStart,
			})
			caseCursor = cEnd
		}

		// The suite span spans its own reported total, but never less than the sum of
		// its cases (caseCursor) nor outside the run window.
		suiteEnd := clamp(suiteStart+secondsToNs(suite.Time), startNs, endNs)
		if suiteEnd < caseCursor {
			suiteEnd = caseCursor
		}
		spans = append(spans, model.Span{
			SpanID:       suiteID,
			TraceID:      traceID,
			ParentSpanID: rootID,
			Name:         suiteName(suite.Name),
			Kind:         model.KindInternal,
			Status:       status,
			StartedNs:    suiteStart,
			EndedNs:      suiteEnd,
			DurationNs:   suiteEnd - suiteStart,
		})
		spans = append(spans, caseSpans...)
		cursor = suiteEnd
	}
	return spans
}

// suiteName falls back to a stable label when a suite has no name attribute, so the
// waterfall never shows a blank row.
func suiteName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "(suite)"
	}
	return name
}

// secondsToNs converts JUnit's fractional-seconds duration to nanoseconds. A
// non-positive or NaN value floors to zero (no backwards span); an absurd value from
// a corrupt report is capped below the int64-nanosecond ceiling so `sec * 1e9` can't
// overflow to a negative/garbage duration (it would wrap span math even though clamp
// bounds the result). maxSec ≈ 285 years, far beyond any real test.
func secondsToNs(sec float64) int64 {
	const maxSec = 9e9 // 9e9 s * 1e9 ns/s = 9e18 ns < math.MaxInt64 (~9.22e18)
	if !(sec > 0) {    // false for <= 0 and NaN
		return 0
	}
	if sec > maxSec {
		sec = maxSec
	}
	return int64(sec * 1e9)
}

// clamp holds ns within [lo, hi].
func clamp(ns, lo, hi int64) int64 {
	if ns < lo {
		return lo
	}
	if ns > hi {
		return hi
	}
	return ns
}
