package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/saagpatel/grotto/internal/model"
)

// goTestAdapter implements Adapter for `go test`. It injects -json and parses the
// resulting event stream from stdout into a package → test span tree, so a slow
// test run becomes a waterfall of which packages and tests dominated.
type goTestAdapter struct{}

// Name returns "go-test", the registry key and Trace.Source for go-test runs.
func (goTestAdapter) Name() string { return "go-test" }

// CapturesStdout is true: `go test -json` writes its event stream to stdout, so
// Run captures it (and suppresses the raw-JSON passthrough).
func (goTestAdapter) CapturesStdout() bool { return true }

// jsonFlag activates go test's machine-readable event stream.
const jsonFlag = "-json"

// PrepareArgv appends -json to the go test invocation if it is not already
// present, so the child emits the event stream the adapter parses.
func (goTestAdapter) PrepareArgv(argv []string) []string {
	for _, arg := range argv {
		if arg == jsonFlag {
			return argv
		}
	}
	return append(argv, jsonFlag)
}

// testEvent is one line of `go test -json` output. Test is empty for
// package-level events; Elapsed (seconds) is set on pass/fail/skip. Time is the
// event's absolute wall-clock timestamp (RFC3339), used in preference to the
// ms-rounded Elapsed for span boundaries.
type testEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

// spanBuilder accumulates the start/end/status of a package or test span as
// events arrive, before it is materialized into a model.Span.
type spanBuilder struct {
	id       string
	parentID string
	name     string
	startNs  int64
	endNs    int64
	started  bool
	ended    bool
	status   model.StatusCode
}

// ParseSpans turns the captured `go test -json` event stream into a package →
// test span tree parented under bc.RootID. Returns (nil, nil) when the stream is
// empty or unparseable, so a run degrades to the opaque root span. Times are
// anchored within [bc.StartNs, bc.EndNs]; a failed test or package carries error
// status. Tests whose stream lacks an end event (panic/timeout) end at bc.EndNs.
func (goTestAdapter) ParseSpans(_ context.Context, bc BuildContext) ([]model.Span, error) {
	if len(bc.Stdout) == 0 {
		return nil, nil
	}

	pkgs := make(map[string]*spanBuilder)
	tests := make(map[string]*spanBuilder) // key: package + "\x00" + test
	// pkgOf ensures a package span exists (tests need a parent even if the
	// package-level start event has not been seen yet).
	pkgOf := func(name string) *spanBuilder {
		b, ok := pkgs[name]
		if !ok {
			b = &spanBuilder{id: bc.NewSpanID(), parentID: bc.RootID, name: name, status: model.StatusOk}
			pkgs[name] = b
		}
		return b
	}

	sc := bufio.NewScanner(bytes.NewReader(bc.Stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // test output lines can be long
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue // non-JSON lines (e.g. a build error printed before the stream)
		}
		var e testEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		ts := clampNs(parseEventTime(e.Time), bc.StartNs, bc.EndNs)

		var b *spanBuilder
		if e.Test == "" {
			b = pkgOf(e.Package)
		} else {
			b = tests[testKey(e.Package, e.Test)]
			if b == nil {
				b = &spanBuilder{id: bc.NewSpanID(), parentID: pkgOf(e.Package).id, name: e.Test, status: model.StatusOk}
				tests[testKey(e.Package, e.Test)] = b
			}
		}
		applyEvent(b, e.Action, ts)
	}

	return materialize(pkgs, tests, bc.StartNs, bc.EndNs), nil
}

func testKey(pkg, test string) string { return pkg + "\x00" + test }

// applyEvent folds one event's action and timestamp into a span builder.
func applyEvent(b *spanBuilder, action string, ts int64) {
	switch action {
	case "start", "run":
		if !b.started {
			b.startNs, b.started = ts, true
		}
	case "pass", "fail", "skip":
		b.endNs, b.ended = ts, true
		if action == "fail" {
			b.status = model.StatusError
		}
	}
}

// materialize converts the accumulated builders into spans, filling missing
// bounds from the run window so an incomplete stream still yields valid spans.
func materialize(pkgs, tests map[string]*spanBuilder, startNs, endNs int64) []model.Span {
	out := make([]model.Span, 0, len(pkgs)+len(tests))
	emit := func(b *spanBuilder) {
		if !b.started {
			b.startNs = startNs
		}
		if !b.ended || b.endNs < b.startNs {
			b.endNs = endNs
		}
		out = append(out, model.Span{
			SpanID:       b.id,
			ParentSpanID: b.parentID,
			Name:         b.name,
			Kind:         model.KindInternal,
			Status:       b.status,
			StartedNs:    b.startNs,
			EndedNs:      b.endNs,
			DurationNs:   b.endNs - b.startNs,
		})
	}
	for _, b := range pkgs {
		emit(b)
	}
	for _, b := range tests {
		emit(b)
	}
	return out
}

// parseEventTime parses go test's RFC3339 timestamp to Unix nanoseconds, or
// returns 0 when absent/unparseable (clampNs then anchors it to the run start).
func parseEventTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// clampNs holds ns within [lo, hi]; a zero/out-of-range value snaps to lo.
func clampNs(ns, lo, hi int64) int64 {
	if ns < lo {
		return lo
	}
	if ns > hi {
		return hi
	}
	return ns
}
