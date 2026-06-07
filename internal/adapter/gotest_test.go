package adapter

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saagpatel/grotto/internal/model"
)

func TestGoTestAdapter_Basics(t *testing.T) {
	a := goTestAdapter{}
	if a.Name() != "go-test" {
		t.Errorf("Name() = %q, want go-test", a.Name())
	}
	if !a.CapturesStdout() {
		t.Error("go-test reads stdout, CapturesStdout must be true")
	}
	got := a.PrepareArgv([]string{"go", "test", "./..."}, "")
	if len(got) == 0 || got[len(got)-1] != "-json" {
		t.Errorf("PrepareArgv must append -json, got %v", got)
	}
	if idem := a.PrepareArgv([]string{"go", "test", "-json"}, ""); len(idem) != 3 {
		t.Errorf("PrepareArgv must be idempotent, got %v", idem)
	}
}

func TestGoTestAdapter_ParseSpans(t *testing.T) {
	mustNs := func(s string) int64 {
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return tm.UnixNano()
	}
	stream := strings.Join([]string{
		`{"Time":"2026-06-06T05:08:39.10Z","Action":"start","Package":"pkg/a"}`,
		`{"Time":"2026-06-06T05:08:39.11Z","Action":"run","Package":"pkg/a","Test":"TestPass"}`,
		`{"Time":"2026-06-06T05:08:39.15Z","Action":"pass","Package":"pkg/a","Test":"TestPass","Elapsed":0.04}`,
		`{"Time":"2026-06-06T05:08:39.16Z","Action":"run","Package":"pkg/a","Test":"TestFail"}`,
		`{"Time":"2026-06-06T05:08:39.20Z","Action":"fail","Package":"pkg/a","Test":"TestFail","Elapsed":0.04}`,
		`{"Time":"2026-06-06T05:08:39.25Z","Action":"run","Package":"pkg/a","Test":"TestHang"}`, // no end event
		`{"Time":"2026-06-06T05:08:39.30Z","Action":"fail","Package":"pkg/a","Elapsed":0.2}`,
		``,                        // blank line tolerated
		`not json — a build line`, // non-JSON tolerated
	}, "\n")

	n := 0
	ids := func() string { n++; return "s" + strconv.Itoa(n) }
	bc := BuildContext{
		RootID:    "root",
		TraceID:   "tr",
		StartNs:   mustNs("2026-06-06T05:08:39.00Z"),
		EndNs:     mustNs("2026-06-06T05:08:39.30Z"),
		Stdout:    []byte(stream),
		NewSpanID: ids,
	}

	spans, err := goTestAdapter{}.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Fatalf("ParseSpans: %v", err)
	}
	byName := make(map[string]model.Span, len(spans))
	for _, s := range spans {
		byName[s.Name] = s
	}

	pkg, ok := byName["pkg/a"]
	if !ok {
		t.Fatal("missing package span pkg/a")
	}
	if pkg.ParentSpanID != "root" {
		t.Errorf("package parent = %q, want root", pkg.ParentSpanID)
	}
	if pkg.Status != model.StatusError {
		t.Error("package with a failing test must carry error status")
	}

	pass, ok := byName["TestPass"]
	if !ok {
		t.Fatal("missing TestPass span")
	}
	if pass.ParentSpanID != pkg.SpanID {
		t.Errorf("TestPass parent = %q, want package %q", pass.ParentSpanID, pkg.SpanID)
	}
	if pass.DurationNs != 40*int64(time.Millisecond) {
		t.Errorf("TestPass duration = %d, want 40ms", pass.DurationNs)
	}
	if pass.Status != model.StatusOk {
		t.Error("TestPass must be StatusOk")
	}

	if fail, ok := byName["TestFail"]; !ok || fail.Status != model.StatusError {
		t.Errorf("TestFail must exist with error status, got %+v (ok=%v)", byName["TestFail"], ok)
	}

	// TestHang has a run but no end event — it must close at the run window end.
	hang, ok := byName["TestHang"]
	if !ok {
		t.Fatal("missing TestHang span")
	}
	if hang.EndedNs != bc.EndNs {
		t.Errorf("incomplete TestHang must end at EndNs %d, got %d", bc.EndNs, hang.EndedNs)
	}
}

func TestGoTestAdapter_PackageLabel(t *testing.T) {
	tests := []struct {
		pkg  string
		want string
	}{
		{pkg: "github.com/saagpatel/grotto", want: "."},
		{pkg: "github.com/saagpatel/grotto/internal/render", want: "internal/render"},
		{pkg: "example.com/other/pkg", want: "example.com/other/pkg"},
	}
	for _, tt := range tests {
		if got := goTestPackageLabel(tt.pkg); got != tt.want {
			t.Errorf("goTestPackageLabel(%q) = %q, want %q", tt.pkg, got, tt.want)
		}
	}
}

func TestGoTestAdapter_ParseSpansShortensModulePackageLabel(t *testing.T) {
	parseNs := func(s string) int64 {
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return tm.UnixNano()
	}
	stream := strings.Join([]string{
		`{"Time":"2026-06-06T05:08:39.10Z","Action":"start","Package":"github.com/saagpatel/grotto/internal/render"}`,
		`{"Time":"2026-06-06T05:08:39.11Z","Action":"run","Package":"github.com/saagpatel/grotto/internal/render","Test":"TestTree"}`,
		`{"Time":"2026-06-06T05:08:39.15Z","Action":"pass","Package":"github.com/saagpatel/grotto/internal/render","Test":"TestTree","Elapsed":0.04}`,
		`{"Time":"2026-06-06T05:08:39.16Z","Action":"pass","Package":"github.com/saagpatel/grotto/internal/render","Elapsed":0.06}`,
	}, "\n")

	next := 0
	spans, err := goTestAdapter{}.ParseSpans(context.Background(), BuildContext{
		RootID:  "root",
		StartNs: parseNs("2026-06-06T05:08:39.00Z"),
		EndNs:   parseNs("2026-06-06T05:08:39.30Z"),
		Stdout:  []byte(stream),
		NewSpanID: func() string {
			next++
			return "s" + strconv.Itoa(next)
		},
	})
	if err != nil {
		t.Fatalf("ParseSpans: %v", err)
	}

	byName := make(map[string]model.Span, len(spans))
	for _, span := range spans {
		byName[span.Name] = span
	}
	pkg, ok := byName["internal/render"]
	if !ok {
		t.Fatalf("missing shortened package span; spans=%+v", spans)
	}
	if _, ok := byName["github.com/saagpatel/grotto/internal/render"]; ok {
		t.Fatalf("full import path should not be used as display name; spans=%+v", spans)
	}
	test, ok := byName["TestTree"]
	if !ok {
		t.Fatal("missing TestTree span")
	}
	if test.ParentSpanID != pkg.SpanID {
		t.Errorf("TestTree parent = %q, want shortened package span %q", test.ParentSpanID, pkg.SpanID)
	}
}

// TestGoTestStream_MatchesBufferedParse is the streaming-refactor safety net: the
// live driver (NewStream + Consume per line + Finalize) must produce byte-identical
// spans to the buffered driver (ParseSpans over the whole stream). Because both feed
// builders in the same line order with identical fresh ID counters, the same logical
// span gets the same ID; only materialize's map-iteration order differs, so sorting
// by SpanID makes the two directly comparable. This is what lets Run trust streaming.
func TestGoTestStream_MatchesBufferedParse(t *testing.T) {
	parseNs := func(s string) int64 {
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return tm.UnixNano()
	}
	lines := []string{
		`{"Time":"2026-06-06T05:08:39.10Z","Action":"start","Package":"pkg/a"}`,
		`{"Time":"2026-06-06T05:08:39.11Z","Action":"run","Package":"pkg/a","Test":"TestPass"}`,
		`{"Time":"2026-06-06T05:08:39.12Z","Action":"output","Package":"pkg/a","Test":"TestPass"}`, // ignored event, no builder
		`{"Time":"2026-06-06T05:08:39.15Z","Action":"pass","Package":"pkg/a","Test":"TestPass","Elapsed":0.04}`,
		`{"Time":"2026-06-06T05:08:39.16Z","Action":"run","Package":"pkg/b","Test":"TestFail"}`,
		`{"Time":"2026-06-06T05:08:39.20Z","Action":"fail","Package":"pkg/b","Test":"TestFail","Elapsed":0.04}`,
		`{"Time":"2026-06-06T05:08:39.25Z","Action":"run","Package":"pkg/b","Test":"TestHang"}`, // no end event
		`{"Time":"2026-06-06T05:08:39.30Z","Action":"fail","Package":"pkg/b","Elapsed":0.2}`,
		``,         // blank line tolerated by both drivers
		`not json`, // non-JSON tolerated by both drivers
	}
	start := parseNs("2026-06-06T05:08:39.00Z")
	end := parseNs("2026-06-06T05:08:39.30Z")

	// Buffered driver over the joined stream.
	cnt1 := 0
	bc := BuildContext{
		RootID: "root", TraceID: "tr", StartNs: start, EndNs: end,
		Stdout:    []byte(strings.Join(lines, "\n")),
		NewSpanID: func() string { cnt1++; return "s" + strconv.Itoa(cnt1) },
	}
	buffered, err := goTestAdapter{}.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Fatalf("buffered ParseSpans: %v", err)
	}

	// Streaming driver fed line by line, with an identical fresh ID counter.
	cnt2 := 0
	s := goTestAdapter{}.NewStream(StreamInit{
		RootID: "root", TraceID: "tr", StartNs: start,
		NewSpanID: func() string { cnt2++; return "s" + strconv.Itoa(cnt2) },
	})
	for _, ln := range lines {
		s.Consume([]byte(ln))
	}
	streamed := s.Finalize(end)

	sortBySpanID := func(spans []model.Span) {
		sort.Slice(spans, func(i, j int) bool { return spans[i].SpanID < spans[j].SpanID })
	}
	sortBySpanID(buffered)
	sortBySpanID(streamed)

	if len(buffered) == 0 {
		t.Fatal("expected spans from the buffered driver, got none")
	}
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("streaming parse diverged from buffered:\n buffered=%+v\n streamed=%+v", buffered, streamed)
	}
}
