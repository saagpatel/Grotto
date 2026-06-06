package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/saagpatel/grotto/internal/model"
)

func TestJUnitAdapter_Basics(t *testing.T) {
	a := junitAdapter{}
	if a.Name() != "junit" {
		t.Errorf("Name() = %q, want junit", a.Name())
	}
	if a.CapturesStdout() {
		t.Error("junit reads a file, not stdout; CapturesStdout must be false")
	}
}

func TestJUnitAdapter_PrepareArgv(t *testing.T) {
	a := junitAdapter{}
	dir := "/tmp/scratch"
	want := "--junitxml=" + filepath.Join(dir, junitReportName)

	t.Run("injects junitxml pointed at scratch dir", func(t *testing.T) {
		got := a.PrepareArgv([]string{"pytest", "-q"}, dir)
		if got[len(got)-1] != want {
			t.Errorf("last arg = %q, want %q", got[len(got)-1], want)
		}
	})

	t.Run("strips a user --junitxml=path and substitutes grotto's", func(t *testing.T) {
		got := a.PrepareArgv([]string{"pytest", "--junitxml=/user/out.xml", "-q"}, dir)
		for _, arg := range got[:len(got)-1] {
			if arg == "--junitxml=/user/out.xml" {
				t.Errorf("user --junitxml not stripped: %v", got)
			}
		}
		if got[len(got)-1] != want {
			t.Errorf("last arg = %q, want grotto path %q", got[len(got)-1], want)
		}
	})

	t.Run("strips a separate-token --junit-xml path and its value", func(t *testing.T) {
		got := a.PrepareArgv([]string{"pytest", "--junit-xml", "/user/out.xml", "-q"}, dir)
		for _, arg := range got {
			if arg == "/user/out.xml" || arg == "--junit-xml" {
				t.Errorf("separate-token junit flag not fully stripped: %v", got)
			}
		}
		if got[len(got)-1] != want {
			t.Errorf("last arg = %q, want grotto path %q", got[len(got)-1], want)
		}
	})
}

// writeReport drops a JUnit XML report into a fresh scratch dir and returns the dir,
// mirroring how grotto's PrepareArgv tells pytest to write it.
func writeReport(t *testing.T, xml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, junitReportName), []byte(xml), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	return dir
}

func TestJUnitAdapter_ParseSpans_LayoutAndNesting(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="utf-8"?>
<testsuites name="pytest tests">
  <testsuite name="suite-one" tests="2" time="0.060">
    <testcase classname="test_smoke" name="test_a" time="0.001" />
    <testcase classname="test_smoke" name="test_b" time="0.053" />
  </testsuite>
</testsuites>`

	const start = int64(1_000_000_000)
	const end = int64(2_000_000_000)
	n := 0
	bc := BuildContext{
		RootID:     "root",
		TraceID:    "tr",
		StartNs:    start,
		EndNs:      end,
		ScratchDir: writeReport(t, xml),
		NewSpanID:  func() string { n++; return "s" + strconv.Itoa(n) },
	}

	spans, err := junitAdapter{}.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Fatalf("ParseSpans: %v", err)
	}
	byName := make(map[string]model.Span, len(spans))
	for _, s := range spans {
		if s.TraceID != "tr" {
			t.Errorf("span %q TraceID = %q, want tr", s.Name, s.TraceID)
		}
		byName[s.Name] = s
	}

	suite, ok := byName["suite-one"]
	if !ok {
		t.Fatal("missing suite span")
	}
	if suite.ParentSpanID != "root" {
		t.Errorf("suite parent = %q, want root", suite.ParentSpanID)
	}

	a, b := byName["test_a"], byName["test_b"]
	if a.ParentSpanID != suite.SpanID || b.ParentSpanID != suite.SpanID {
		t.Errorf("test cases must parent to the suite span %q; got a=%q b=%q", suite.SpanID, a.ParentSpanID, b.ParentSpanID)
	}
	if a.DurationNs != 1*int64(time.Millisecond) {
		t.Errorf("test_a duration = %d, want 1ms", a.DurationNs)
	}
	if b.DurationNs != 53*int64(time.Millisecond) {
		t.Errorf("test_b duration = %d, want 53ms", b.DurationNs)
	}
	// JUnit has no start times, so cases lay out sequentially: b starts where a ends.
	if b.StartedNs != a.EndedNs {
		t.Errorf("test_b must start where test_a ends: a.End=%d b.Start=%d", a.EndedNs, b.StartedNs)
	}
	if a.StartedNs != start {
		t.Errorf("first case must start at the run start %d, got %d", start, a.StartedNs)
	}
}

func TestJUnitAdapter_ParseSpans_StatusAndSingleSuiteRoot(t *testing.T) {
	// A bare <testsuite> root (no <testsuites> wrapper) with a failure and a skip.
	const xml = `<testsuite name="solo" tests="3" time="0.030">
  <testcase name="ok" time="0.010" />
  <testcase name="boom" time="0.010"><failure message="assert">trace</failure></testcase>
  <testcase name="meh" time="0.010"><skipped message="cond" /></testcase>
</testsuite>`

	n := 0
	bc := BuildContext{
		RootID: "root", TraceID: "tr", StartNs: 0, EndNs: 1_000_000_000,
		ScratchDir: writeReport(t, xml),
		NewSpanID:  func() string { n++; return "s" + strconv.Itoa(n) },
	}
	spans, err := junitAdapter{}.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Fatalf("ParseSpans: %v", err)
	}
	byName := make(map[string]model.Span, len(spans))
	for _, s := range spans {
		byName[s.Name] = s
	}

	if byName["ok"].Status != model.StatusOk {
		t.Error("passing case must be StatusOk")
	}
	if byName["boom"].Status != model.StatusError {
		t.Error("failing case must be StatusError")
	}
	if byName["meh"].Status != model.StatusOk {
		t.Error("skipped case is not a failure; must be StatusOk")
	}
	if byName["solo"].Status != model.StatusError {
		t.Error("a suite with any failing case must carry error status")
	}
}

func TestJUnitAdapter_ParseSpans_MissingReportIsBenign(t *testing.T) {
	// No report written: a crash before flush, or a runner that ignored --junitxml.
	bc := BuildContext{
		RootID: "root", TraceID: "tr", StartNs: 0, EndNs: 1_000,
		ScratchDir: t.TempDir(), // empty dir, no junit.xml
		NewSpanID:  func() string { return "x" },
	}
	spans, err := junitAdapter{}.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Errorf("missing report must be benign, got err=%v", err)
	}
	if spans != nil {
		t.Errorf("missing report must yield nil spans, got %d", len(spans))
	}
}

func TestJUnitAdapter_ParseSpans_MalformedReportErrors(t *testing.T) {
	bc := BuildContext{
		RootID: "root", TraceID: "tr", StartNs: 0, EndNs: 1_000,
		ScratchDir: writeReport(t, "<testsuite><not-closed>"),
		NewSpanID:  func() string { return "x" },
	}
	if _, err := (junitAdapter{}).ParseSpans(context.Background(), bc); err == nil {
		t.Error("a present-but-malformed report must return an error so collect can warn")
	}
}

func TestJUnitAdapter_ParseSpans_UnexpectedRootErrors(t *testing.T) {
	// A well-formed XML doc whose root is neither testsuites nor testsuite must be a
	// clear error, not a silent zero-span result.
	bc := BuildContext{
		RootID: "root", TraceID: "tr", StartNs: 0, EndNs: 1_000,
		ScratchDir: writeReport(t, `<results><case/></results>`),
		NewSpanID:  func() string { return "x" },
	}
	if _, err := (junitAdapter{}).ParseSpans(context.Background(), bc); err == nil {
		t.Error("an unexpected root element must return an error, not nil spans")
	}
}
