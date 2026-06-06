package adapter

import (
	"context"
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
	got := a.PrepareArgv([]string{"go", "test", "./..."})
	if len(got) == 0 || got[len(got)-1] != "-json" {
		t.Errorf("PrepareArgv must append -json, got %v", got)
	}
	if idem := a.PrepareArgv([]string{"go", "test", "-json"}); len(idem) != 3 {
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
