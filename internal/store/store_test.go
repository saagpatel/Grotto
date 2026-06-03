package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

// sixSpanTrace returns a 6-span trace already ordered the way GetTrace returns
// it (spans by started_ns, attributes by key) so a round trip is byte-identical.
func sixSpanTrace() model.Trace {
	spans := []model.Span{
		{
			SpanID: "s1", TraceID: "trace-1", ParentSpanID: "", Name: "build",
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: 0, EndedNs: 600, DurationNs: 600,
			Attributes: []model.Attribute{
				{Key: "command", ValueType: "str", Value: "make all"},
				{Key: "jobs", ValueType: "int", Value: "8"},
			},
		},
		{SpanID: "s2", TraceID: "trace-1", ParentSpanID: "s1", Name: "compile", Kind: model.KindInternal, Status: model.StatusOk, StartedNs: 10, EndedNs: 200, DurationNs: 190},
		{SpanID: "s4", TraceID: "trace-1", ParentSpanID: "s2", Name: "parse", Kind: model.KindInternal, Status: model.StatusOk, StartedNs: 20, EndedNs: 90, DurationNs: 70},
		{SpanID: "s5", TraceID: "trace-1", ParentSpanID: "s2", Name: "codegen", Kind: model.KindInternal, Status: model.StatusError, StartedNs: 100, EndedNs: 190, DurationNs: 90},
		{
			SpanID: "s3", TraceID: "trace-1", ParentSpanID: "s1", Name: "link",
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: 210, EndedNs: 400, DurationNs: 190,
			Attributes: []model.Attribute{{Key: "cached", ValueType: "bool", Value: "false"}},
		},
		{SpanID: "s6", TraceID: "trace-1", ParentSpanID: "s3", Name: "strip", Kind: model.KindInternal, Status: model.StatusOk, StartedNs: 410, EndedNs: 590, DurationNs: 180},
	}
	return model.Trace{
		TraceID: "trace-1", RunLabel: "make all", Source: "mark", RootName: "build",
		StartedNs: 0, EndedNs: 600, DurationNs: 600, SpanCount: 6, Spans: spans,
	}
}

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st, ctx
}

func TestStore_TraceRoundTrip(t *testing.T) {
	st, ctx := newTestStore(t)

	want := sixSpanTrace()
	require.NoError(t, st.InsertTrace(ctx, want))

	got, err := st.GetTrace(ctx, want.TraceID)
	require.NoError(t, err)
	assert.Equal(t, want, got, "stored trace must round-trip identically")
}

func TestStore_GetTraceNotFound(t *testing.T) {
	st, ctx := newTestStore(t)

	_, err := st.GetTrace(ctx, "does-not-exist")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestStore_ForeignKeyEnforced(t *testing.T) {
	st, ctx := newTestStore(t)

	// Inserting a span whose trace is absent must fail when foreign keys are on,
	// proving the per-connection pragma took effect.
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO spans (span_id, trace_id, parent_span_id, name, kind, status_code, started_ns, ended_ns, duration_ns)
		 VALUES ('x', 'missing-trace', NULL, 'n', 0, 0, 0, 0, 0)`)
	require.Error(t, err, "foreign key constraint must reject an orphan span")
}
