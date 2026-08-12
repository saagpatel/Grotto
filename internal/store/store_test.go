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
			Links: []model.SpanLink{{
				TraceID: "prior-trace", SpanID: "prior-span", TraceState: "vendor=test",
				Attributes:             []model.Attribute{{Key: "gen_ai.response.id", ValueType: "str", Value: "resp_prior"}},
				DroppedAttributesCount: 1, Flags: 1,
			}},
			DroppedAttributesCount: 2,
			DroppedLinksCount:      3,
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

// minimalTrace builds a one-span (root only) trace for ordering/listing tests.
func minimalTrace(id, label string, startedNs int64) model.Trace {
	return model.Trace{
		TraceID: id, RunLabel: label, Source: "mark", RootName: "root",
		StartedNs: startedNs, EndedNs: startedNs + 100, DurationNs: 100, SpanCount: 1,
		Spans: []model.Span{{
			SpanID: id + "-root", TraceID: id, Name: "root",
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: startedNs, EndedNs: startedNs + 100, DurationNs: 100,
		}},
	}
}

func TestStore_RecentTracesNewestFirst(t *testing.T) {
	st, ctx := newTestStore(t)

	// Insert out of start-time order; RecentTraces must return newest start first.
	require.NoError(t, st.InsertTrace(ctx, minimalTrace("t-mid", "middle", 200)))
	require.NoError(t, st.InsertTrace(ctx, minimalTrace("t-old", "oldest", 100)))
	require.NoError(t, st.InsertTrace(ctx, minimalTrace("t-new", "newest", 300)))

	got, err := st.RecentTraces(ctx, 50)
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, []string{"t-new", "t-mid", "t-old"},
		[]string{got[0].TraceID, got[1].TraceID, got[2].TraceID},
		"summaries must be ordered newest start time first")

	// Summary fields are populated from the traces row.
	assert.Equal(t, "newest", got[0].RunLabel)
	assert.Equal(t, "mark", got[0].Source)
	assert.Equal(t, 1, got[0].SpanCount)
	assert.Equal(t, int64(300), got[0].StartedNs)
	assert.Equal(t, int64(100), got[0].DurationNs)
	assert.Positive(t, got[0].CreatedAt, "created_at is stamped at write time")
}

func TestStore_RecentTracesLimit(t *testing.T) {
	st, ctx := newTestStore(t)
	for i := int64(0); i < 5; i++ {
		require.NoError(t, st.InsertTrace(ctx, minimalTrace(
			"t"+string(rune('a'+i)), "run", i*10)))
	}

	got, err := st.RecentTraces(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, got, 2, "limit must cap the number of summaries")
}

func TestStore_RecentTracesEmptyAndNonPositiveLimit(t *testing.T) {
	st, ctx := newTestStore(t)

	empty, err := st.RecentTraces(ctx, 50)
	require.NoError(t, err)
	assert.Empty(t, empty, "no traces stored yields no summaries")

	require.NoError(t, st.InsertTrace(ctx, minimalTrace("t1", "run", 0)))
	none, err := st.RecentTraces(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, none, "a non-positive limit yields no summaries")
}
