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
func (goTestAdapter) PrepareArgv(argv []string, _ string) []string {
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

// goTestStream is the streaming state machine for `go test -json`: it folds each
// event line into a package → test span tree as the stream arrives, retaining only
// the derived span builders rather than the raw output. It is the single parse core
// shared by both drivers — the live stdout pump (collect's lineWriter) and the
// buffered ParseSpans — so the two paths can never diverge. Times are lower-clamped
// to the run start as events arrive; the upper bound is applied in Finalize once the
// run end is known (during streaming, endNs is not yet available).
type goTestStream struct {
	rootID    string
	startNs   int64
	newSpanID func() string
	pkgs      map[string]*spanBuilder
	tests     map[testID]*spanBuilder
}

// testID identifies a test span by its package and name. A struct map key avoids
// the per-event string concatenation a composite string key would cost — Consume
// runs once per stream line, so this is on the hot path.
type testID struct{ pkg, test string }

// NewStream begins a streaming `go test -json` parse seeded from init. goTestAdapter
// implements adapter.StreamAdapter via this method, so Run consumes its stdout live.
func (goTestAdapter) NewStream(init StreamInit) StreamParser {
	return newGoTestStream(init.RootID, init.StartNs, init.NewSpanID)
}

func newGoTestStream(rootID string, startNs int64, newSpanID func() string) *goTestStream {
	return &goTestStream{
		rootID:    rootID,
		startNs:   startNs,
		newSpanID: newSpanID,
		pkgs:      make(map[string]*spanBuilder),
		tests:     make(map[testID]*spanBuilder),
	}
}

// pkgOf ensures a package span builder exists (tests need a parent even if the
// package-level start event has not been seen yet).
func (s *goTestStream) pkgOf(name string) *spanBuilder {
	b, ok := s.pkgs[name]
	if !ok {
		b = &spanBuilder{id: s.newSpanID(), parentID: s.rootID, name: name, status: model.StatusOk}
		s.pkgs[name] = b
	}
	return b
}

// actionOutput marks a `go test -json` "output" event — every line a test prints,
// and the high-volume majority of the stream on a verbose suite. The adapter models
// only start/run/pass/fail/skip, so output events are discarded; matching the marker
// as a substring lets Consume skip the full JSON decode + timestamp parse for them.
// Only output events carry arbitrary free text, so this marker cannot appear inside
// a structural event — a hit unambiguously means "discard". In real `go test -json`
// a package's first event is "start" and a test's is "run", so an output event never
// introduces a span; skipping it can only drop work, never a span.
var actionOutput = []byte(`"Action":"output"`)

// Consume folds one line of `go test -json` output into the span tree. Blank lines,
// non-JSON lines (build output), and unmodeled events (output) are ignored — which
// is exactly why streaming wins: those discarded bytes never accumulate.
func (s *goTestStream) Consume(line []byte) {
	t := bytes.TrimSpace(line)
	if len(t) == 0 || t[0] != '{' {
		return
	}
	if bytes.Contains(t, actionOutput) {
		return // discarded event type: skip the decode + time parse entirely
	}
	var e testEvent
	if json.Unmarshal(t, &e) != nil {
		return
	}
	ts := parseEventTime(e.Time)
	if ts < s.startNs {
		ts = s.startNs // lower clamp; the upper clamp is applied in Finalize
	}
	var b *spanBuilder
	if e.Test == "" {
		b = s.pkgOf(e.Package)
	} else {
		key := testID{pkg: e.Package, test: e.Test}
		b = s.tests[key]
		if b == nil {
			b = &spanBuilder{id: s.newSpanID(), parentID: s.pkgOf(e.Package).id, name: e.Test, status: model.StatusOk}
			s.tests[key] = b
		}
	}
	applyEvent(b, e.Action, ts)
}

// Finalize materializes the accumulated builders into spans, filling missing bounds
// from the run window and upper-clamping every bound to endNs.
func (s *goTestStream) Finalize(endNs int64) []model.Span {
	return materialize(s.pkgs, s.tests, s.startNs, endNs)
}

// ParseSpans turns a captured `go test -json` event stream into a package → test
// span tree parented under bc.RootID. It is the buffered driver: it feeds bc.Stdout
// line by line through the same goTestStream the live path uses. Returns (nil, nil)
// when the stream is empty, so a run degrades to the opaque root span. Times are
// anchored within [bc.StartNs, bc.EndNs]; a failed test or package carries error
// status. Tests whose stream lacks an end event (panic/timeout) end at bc.EndNs.
func (a goTestAdapter) ParseSpans(_ context.Context, bc BuildContext) ([]model.Span, error) {
	if len(bc.Stdout) == 0 {
		return nil, nil
	}
	s := newGoTestStream(bc.RootID, bc.StartNs, bc.NewSpanID)
	// Read line by line with no length cap (bufio.Scanner would silently drop
	// everything after a line larger than its buffer — a huge test log dump). A
	// trailing line without a newline is delivered alongside io.EOF, so consume
	// before breaking.
	r := bufio.NewReader(bytes.NewReader(bc.Stdout))
	for {
		line, err := r.ReadBytes('\n')
		s.Consume(line)
		if err != nil {
			break // io.EOF (or a read error): no more lines
		}
	}
	return s.Finalize(bc.EndNs), nil
}

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
func materialize(pkgs map[string]*spanBuilder, tests map[testID]*spanBuilder, startNs, endNs int64) []model.Span {
	out := make([]model.Span, 0, len(pkgs)+len(tests))
	emit := func(b *spanBuilder) {
		if !b.started {
			b.startNs = startNs
		}
		// Pin every bound inside the run window. Consume lower-clamps event times to
		// startNs as they arrive; the upper bound waits until here because endNs is
		// unknown until the command exits. Real go test times fall inside the window,
		// so these guards only fire on a clock skew or a corrupt stream.
		if b.startNs > endNs {
			b.startNs = endNs
		}
		if !b.ended || b.endNs < b.startNs {
			b.endNs = endNs
		}
		if b.endNs > endNs {
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
// returns 0 when absent/unparseable (Consume's lower clamp then anchors it to the
// run start).
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
