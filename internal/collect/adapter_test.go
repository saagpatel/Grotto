package collect

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

// stubAdapter is an in-test Adapter that always returns a fixed set of spans,
// letting us verify that Run grafts them correctly without needing a real build
// tool or a timing report on disk.
type stubAdapter struct {
	name      string
	spanNames []string // names for spans produced in ParseSpans
}

func (s *stubAdapter) Name() string                       { return s.name }
func (s *stubAdapter) PrepareArgv(argv []string) []string { return argv }
func (s *stubAdapter) CapturesStdout() bool               { return false }
func (s *stubAdapter) ParseSpans(_ context.Context, bc adapter.BuildContext) ([]model.Span, error) {
	// Build spans anchored just after the run's start so they sort after the root
	// when GetTrace orders by start_time, keeping assertions straightforward.
	out := make([]model.Span, len(s.spanNames))
	for i, name := range s.spanNames {
		started := bc.StartNs + int64(i+1)*1_000_000 // +1ms, +2ms, … after root start
		out[i] = model.Span{
			SpanID:       bc.NewSpanID(),
			TraceID:      bc.TraceID,
			ParentSpanID: bc.RootID,
			Name:         name,
			Kind:         model.KindInternal,
			Status:       model.StatusOk,
			StartedNs:    started,
			EndedNs:      started + 1_000_000,
			DurationNs:   1_000_000,
		}
	}
	return out, nil
}

// TestRun_AdapterGraftsSpans verifies the full adapter seam in the collect layer:
//   - Run accepts a non-nil Adapter
//   - The adapter's ParseSpans return value is appended to the stored trace
//   - SpanCount is updated to reflect the extra spans
//   - Trace.Source is set to the adapter's name
//
// The command is `sh -c true` so no marks are emitted; the only child spans in
// the stored trace come from the stub adapter.
func TestRun_AdapterGraftsSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}

	ctx := context.Background()

	stub := &stubAdapter{
		name:      "teststub",
		spanNames: []string{"crate-a", "crate-b"},
	}

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// `sh -c true` exits 0, emits no marks, and is available on every POSIX platform.
	id, err := Run(ctx, st, []string{"sh", "-c", "true"}, stub)
	require.NoError(t, err)

	tr, err := st.GetTrace(ctx, id)
	require.NoError(t, err)

	// 1 root span + 2 stub spans = 3 total.
	assert.Equal(t, 3, tr.SpanCount, "SpanCount must include adapter spans")
	require.Len(t, tr.Spans, 3)

	// The adapter's name must become Trace.Source.
	assert.Equal(t, "teststub", tr.Source, "Trace.Source must be the adapter name")

	// Locate root by empty parent; GetTrace orders by start_time so we can't
	// assume it is index 0 when adapter spans could share a start.
	var root model.Span
	var adapterSpans []model.Span
	for _, s := range tr.Spans {
		if s.ParentSpanID == "" {
			root = s
		} else {
			adapterSpans = append(adapterSpans, s)
		}
	}

	require.NotEmpty(t, root.SpanID, "must find exactly one root span")
	assert.Len(t, adapterSpans, 2, "must find exactly 2 adapter spans")

	for _, s := range adapterSpans {
		assert.Equal(t, root.SpanID, s.ParentSpanID,
			"adapter span %q must parent to root", s.Name)
		assert.Equal(t, tr.TraceID, s.TraceID,
			"adapter span %q must carry the trace ID", s.Name)
	}

	// Confirm both expected names appear in the trace.
	names := make(map[string]bool, len(adapterSpans))
	for _, s := range adapterSpans {
		names[s.Name] = true
	}
	assert.True(t, names["crate-a"], "crate-a span must be in the trace")
	assert.True(t, names["crate-b"], "crate-b span must be in the trace")
}

// errAdapter is an Adapter whose ParseSpans always fails, modeling a malformed or
// truncated timing report.
type errAdapter struct{}

func (errAdapter) Name() string                       { return "errstub" }
func (errAdapter) PrepareArgv(argv []string) []string { return argv }
func (errAdapter) CapturesStdout() bool               { return false }
func (errAdapter) ParseSpans(_ context.Context, _ adapter.BuildContext) ([]model.Span, error) {
	return nil, assert.AnError
}

// TestRun_AdapterParseErrorStillStoresTrace verifies that an adapter parse
// failure does not discard the already-captured trace: the command ran, so its
// root/marks trace must still be stored (the per-unit spans are an enrichment,
// not the capture). Regression guard for the "error nukes the whole trace" bug.
func TestRun_AdapterParseErrorStillStoresTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}

	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := Run(ctx, st, []string{"sh", "-c", "true"}, errAdapter{})
	require.NoError(t, err, "a failed adapter parse must not fail the run")

	tr, err := st.GetTrace(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 1, tr.SpanCount, "base trace (root only) must survive an adapter parse failure")
}

// stdoutCaptureAdapter records the stdout bytes handed to ParseSpans, to prove
// Run captures the child's stdout when CapturesStdout is true.
type stdoutCaptureAdapter struct{ got *[]byte }

func (stdoutCaptureAdapter) Name() string                       { return "stdoutstub" }
func (stdoutCaptureAdapter) PrepareArgv(argv []string) []string { return argv }
func (stdoutCaptureAdapter) CapturesStdout() bool               { return true }
func (s stdoutCaptureAdapter) ParseSpans(_ context.Context, bc adapter.BuildContext) ([]model.Span, error) {
	*s.got = bc.Stdout
	return nil, nil
}

// TestRun_CapturesStdout verifies the CapturesStdout=true path: the child's
// stdout is captured into BuildContext.Stdout (rather than only echoed) so a
// stdout-stream adapter like go-test can parse it.
func TestRun_CapturesStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	var captured []byte
	_, err = Run(ctx, st, []string{"sh", "-c", "printf 'hello-stdout'"}, stdoutCaptureAdapter{got: &captured})
	require.NoError(t, err)
	assert.Equal(t, "hello-stdout", string(captured), "child stdout must reach BuildContext.Stdout")
}

// TestRun_NilAdapterUnchanged verifies that passing nil for the adapter leaves
// the trace exactly as today: only the root span (plus any marks, here zero).
func TestRun_NilAdapterUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}

	ctx := context.Background()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := Run(ctx, st, []string{"sh", "-c", "true"}, nil)
	require.NoError(t, err)

	tr, err := st.GetTrace(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, 1, tr.SpanCount, "nil adapter: only the root span")
	assert.Equal(t, "mark", tr.Source, "nil adapter: source stays mark")
}

// streamStubAdapter is an in-test StreamAdapter: it records every line handed to
// Consume (proving Run drives the stream live, not from a post-exit buffer) and
// emits one span per non-blank line in Finalize. ParseSpans exists only to satisfy
// Adapter and is never reached on the streaming path.
type streamStubAdapter struct{ lines *[]string }

func (streamStubAdapter) Name() string                       { return "streamstub" }
func (streamStubAdapter) PrepareArgv(argv []string) []string { return argv }
func (streamStubAdapter) CapturesStdout() bool               { return true }
func (streamStubAdapter) ParseSpans(context.Context, adapter.BuildContext) ([]model.Span, error) {
	return nil, nil
}
func (s streamStubAdapter) NewStream(init adapter.StreamInit) adapter.StreamParser {
	return &streamStub{init: init, lines: s.lines}
}

type streamStub struct {
	init  adapter.StreamInit
	lines *[]string
}

func (s *streamStub) Consume(line []byte) {
	if t := strings.TrimSpace(string(line)); t != "" {
		*s.lines = append(*s.lines, t)
	}
}

func (s *streamStub) Finalize(_ int64) []model.Span {
	out := make([]model.Span, 0, len(*s.lines))
	for i, name := range *s.lines {
		started := s.init.StartNs + int64(i+1)*1_000_000
		out = append(out, model.Span{
			SpanID:       s.init.NewSpanID(),
			TraceID:      s.init.TraceID,
			ParentSpanID: s.init.RootID,
			Name:         name,
			Kind:         model.KindInternal,
			Status:       model.StatusOk,
			StartedNs:    started,
			EndedNs:      started + 1_000_000,
			DurationNs:   1_000_000,
		})
	}
	return out
}

// TestRun_StreamAdapterConsumesLive verifies the streaming seam end to end: Run
// detects a StreamAdapter, drives its Consume with the child's stdout line by line
// during the run (never buffering the whole stream), then grafts Finalize's spans
// under the assembled root with the trace's ID.
func TestRun_StreamAdapterConsumesLive(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	var lines []string
	stub := streamStubAdapter{lines: &lines}
	id, err := Run(ctx, st, []string{"sh", "-c", "printf 'alpha\\nbeta\\ngamma\\n'"}, stub)
	require.NoError(t, err)

	// Consume saw every stdout line, proving the live (not buffered) path ran.
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, lines, "stream must consume each stdout line")

	tr, err := st.GetTrace(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "streamstub", tr.Source, "Trace.Source must be the adapter name")
	assert.Equal(t, 4, tr.SpanCount, "1 root + 3 streamed spans")

	var root model.Span
	names := make(map[string]bool)
	for _, sp := range tr.Spans {
		if sp.ParentSpanID == "" {
			root = sp
		} else {
			names[sp.Name] = true
		}
	}
	require.NotEmpty(t, root.SpanID, "must find exactly one root span")
	for _, sp := range tr.Spans {
		if sp.ParentSpanID != "" {
			assert.Equal(t, root.SpanID, sp.ParentSpanID, "streamed span %q must parent to root", sp.Name)
			assert.Equal(t, tr.TraceID, sp.TraceID, "streamed span %q must carry the trace ID", sp.Name)
		}
	}
	assert.True(t, names["alpha"] && names["beta"] && names["gamma"], "all three streamed spans must be present")
}
