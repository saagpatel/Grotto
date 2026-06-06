package adapter

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// loadFixture reads the real cargo --timings HTML report captured from a
// multi-crate build (itoa, ryu, smallvec + the local bin). Parsing the genuine
// artifact — not a hand-trimmed one — keeps the test honest to cargo's format.
func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cargo-timing-fixture.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestParseUnits_RealFixture(t *testing.T) {
	units, err := parseUnits(loadFixture(t))
	if err != nil {
		t.Fatalf("parseUnits: %v", err)
	}

	// The fixture build compiled exactly these four units.
	want := []string{"itoa", "probe", "ryu", "smallvec"}
	got := make([]string, 0, len(units))
	for _, u := range units {
		got = append(got, u.Name)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("unit count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unit[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Every unit must carry a version and non-negative, real timing. Versions
	// are not hardcoded — they drift as the registry updates.
	for _, u := range units {
		if u.Version == "" {
			t.Errorf("unit %q has empty version", u.Name)
		}
		if u.Start < 0 || u.Duration < 0 {
			t.Errorf("unit %q has negative timing: start=%v duration=%v", u.Name, u.Start, u.Duration)
		}
	}
}

// TestParseUnits_BracketInString is the load-bearing robustness check: a ']'
// inside a string value must not truncate the array. A naive `];` search would
// fail this; the string-aware scan must pass it.
func TestParseUnits_BracketInString(t *testing.T) {
	html := []byte(`<script>const UNIT_DATA = [` +
		`{"i":0,"name":"weird]name","version":"1.0","start":0.0,"duration":0.1},` +
		`{"i":1,"name":"ok","version":"2.0","start":0.1,"duration":0.2}` +
		`];</script>`)

	units, err := parseUnits(html)
	if err != nil {
		t.Fatalf("parseUnits: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2 (bracket in string truncated the array)", len(units))
	}
	if units[0].Name != "weird]name" {
		t.Errorf("unit[0].Name = %q, want %q", units[0].Name, "weird]name")
	}
}

func TestParseUnits_NoMarker(t *testing.T) {
	if _, err := parseUnits([]byte("<html>no timing data here</html>")); err == nil {
		t.Fatal("expected error when UNIT_DATA marker is absent, got nil")
	}
}

func TestUnitsToSpans_TimeBase(t *testing.T) {
	const startNs = int64(1_000_000_000_000) // arbitrary absolute anchor
	units := []unit{
		{Index: 0, Name: "itoa", Version: "1.0.18", Start: 0.22, Duration: 0.08},
		{Index: 1, Name: "ryu", Version: "1.0.23", Start: 0.22, Duration: 0.08},
	}

	n := 0
	ids := func() string { n++; return "span-" + string(rune('0'+n)) }
	spans := unitsToSpans(units, "rootid", "traceid", startNs, ids)

	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	s := spans[0]
	if s.ParentSpanID != "rootid" || s.TraceID != "traceid" {
		t.Errorf("span parentage wrong: parent=%q trace=%q", s.ParentSpanID, s.TraceID)
	}
	if s.Name != "itoa v1.0.18" {
		t.Errorf("span name = %q, want %q", s.Name, "itoa v1.0.18")
	}
	// 0.22s after the anchor, lasting 0.08s.
	wantStart := startNs + int64(0.22*1e9)
	wantEnd := wantStart + int64(0.08*1e9)
	if s.StartedNs != wantStart || s.EndedNs != wantEnd || s.DurationNs != wantEnd-wantStart {
		t.Errorf("span timing = [%d,%d] dur %d, want [%d,%d] dur %d",
			s.StartedNs, s.EndedNs, s.DurationNs, wantStart, wantEnd, wantEnd-wantStart)
	}
	// Concurrent units (same start) must produce overlapping intervals.
	if spans[1].StartedNs != spans[0].StartedNs {
		t.Errorf("concurrent units should share a start: %d vs %d", spans[1].StartedNs, spans[0].StartedNs)
	}
}

// TestUnitDisplayName_Disambiguation covers the target suffix that keeps the
// three units of a single crate (lib compile, build-script compile, build-script
// run) from rendering as identical duplicate rows.
func TestUnitDisplayName_Disambiguation(t *testing.T) {
	cases := []struct {
		name string
		u    unit
		want string
	}{
		{"plain lib compile (no suffix)", unit{Name: "serde_core", Version: "1.0.228", Target: ""}, "serde_core v1.0.228"},
		{"build-script compile", unit{Name: "serde_core", Version: "1.0.228", Target: " build-script"}, "serde_core v1.0.228 (build-script)"},
		{"build-script run", unit{Name: "serde_core", Version: "1.0.228", Target: " build-script (run)"}, "serde_core v1.0.228 (build-script (run))"},
	}
	for _, tc := range cases {
		if got := tc.u.displayName(); got != tc.want {
			t.Errorf("%s: displayName() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestUnitDAGAttrs verifies the DAG edges are stamped as span attributes:
// cargo.unit always, cargo.unblocks only when the unit has outgoing edges.
func TestUnitDAGAttrs(t *testing.T) {
	withEdges := unit{Index: 5, UnblockedUnits: []int{6, 7, 12}}.dagAttrs()
	got := map[string]string{}
	for _, a := range withEdges {
		got[a.Key] = a.Value
	}
	if got["cargo.unit"] != "5" {
		t.Errorf("cargo.unit = %q, want 5", got["cargo.unit"])
	}
	if got["cargo.unblocks"] != "6,7,12" {
		t.Errorf("cargo.unblocks = %q, want 6,7,12", got["cargo.unblocks"])
	}

	noEdges := unit{Index: 3}.dagAttrs()
	if len(noEdges) != 1 || noEdges[0].Key != "cargo.unit" {
		t.Errorf("a unit with no edges must carry only cargo.unit, got %v", noEdges)
	}
}

// TestFindTimingReportPath covers the plain line, an ANSI-colored line (cargo
// under CLICOLOR_FORCE), and the absent case.
func TestFindTimingReportPath(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{"plain", "   Compiling x\nTiming report saved to /tmp/r.html\n", "/tmp/r.html"},
		{"ansi-wrapped", "\x1b[1mTiming report saved to\x1b[0m /tmp/r.html\n", "/tmp/r.html"},
		{"absent", "   Compiling x\n    Finished\n", ""},
	}
	for _, tc := range cases {
		if got := findTimingReportPath([]byte(tc.stderr)); got != tc.want {
			t.Errorf("%s: findTimingReportPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestUnitsToSpans_ClampsCorruptTiming verifies the defensive clamp: a unit with
// a negative start (which would precede the root) and a negative duration (which
// would invert the interval) is clamped so the span neither precedes startNs nor
// ends before it begins.
func TestUnitsToSpans_ClampsCorruptTiming(t *testing.T) {
	const startNs = int64(1_000_000_000_000)
	units := []unit{{Name: "bad", Version: "0.0.0", Start: -0.5, Duration: -1.0}}

	spans := unitsToSpans(units, "rootid", "traceid", startNs, func() string { return "s1" })
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.StartedNs < startNs {
		t.Errorf("StartedNs %d must not precede startNs %d", s.StartedNs, startNs)
	}
	if s.EndedNs < s.StartedNs || s.DurationNs < 0 {
		t.Errorf("interval must not be inverted: start=%d end=%d dur=%d", s.StartedNs, s.EndedNs, s.DurationNs)
	}
}
