package render

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

// unitSpan builds a cargo-unit span with the DAG attributes the adapter stamps.
func unitSpan(idx int, name string, start, dur int64, unblocks ...int) model.Span {
	attrs := []model.Attribute{{Key: "cargo.unit", ValueType: "int", Value: strconv.Itoa(idx)}}
	if len(unblocks) > 0 {
		ids := make([]string, len(unblocks))
		for i, u := range unblocks {
			ids[i] = strconv.Itoa(u)
		}
		attrs = append(attrs, model.Attribute{Key: "cargo.unblocks", ValueType: "str", Value: strings.Join(ids, ",")})
	}
	return model.Span{
		SpanID: name, Name: name,
		StartedNs: start, EndedNs: start + dur, DurationNs: dur,
		Attributes: attrs,
	}
}

// TestComputeCriticalPath_DiamondPicksLongerBranch builds a diamond DAG
// (A unblocks B and C; both unblock D) where B is far longer than C, and asserts
// the critical path runs A → B → D, not the cheaper A → C → D.
func TestComputeCriticalPath_DiamondPicksLongerBranch(t *testing.T) {
	tr := model.Trace{Spans: []model.Span{
		{SpanID: "root", Name: "cargo"}, // no cargo.unit — must be ignored
		unitSpan(0, "A", 0, 100, 1, 2),
		unitSpan(1, "B", 100, 300, 3),
		unitSpan(2, "C", 100, 50, 3),
		unitSpan(3, "D", 400, 100),
	}}

	cp, ok := ComputeCriticalPath(tr)
	require.True(t, ok)

	gotNames := make([]string, len(cp.Spans))
	for i, s := range cp.Spans {
		gotNames[i] = s.Name
	}
	assert.Equal(t, []string{"A", "B", "D"}, gotNames, "path must take the longer B branch")
	assert.Equal(t, int64(500), cp.CriticalNs, "100 + 300 + 100")
	assert.Equal(t, int64(550), cp.TotalWorkNs, "sum of all four units")
	assert.Equal(t, 4, cp.UnitCount)
}

// TestComputeCriticalPath_NoEdges verifies graceful detection of a trace without
// cargo unit attributes (a marks or OTLP trace).
func TestComputeCriticalPath_NoEdges(t *testing.T) {
	tr := model.Trace{Spans: []model.Span{
		{SpanID: "root", Name: "build"},
		{SpanID: "s1", Name: "compile", ParentSpanID: "root"},
	}}
	_, ok := ComputeCriticalPath(tr)
	assert.False(t, ok, "a trace with no cargo.unit attributes has no critical path")
}

// TestComputeCriticalPath_ZeroDurationPredecessorKeepsChain guards the chain
// against a zero-duration predecessor being skipped during backtracking.
func TestComputeCriticalPath_ZeroDurationPredecessorKeepsChain(t *testing.T) {
	tr := model.Trace{Spans: []model.Span{
		unitSpan(0, "fast", 0, 0, 1), // zero duration, but unblocks the next
		unitSpan(1, "slow", 0, 200),
	}}
	cp, ok := ComputeCriticalPath(tr)
	require.True(t, ok)
	names := []string{cp.Spans[0].Name, cp.Spans[len(cp.Spans)-1].Name}
	assert.Equal(t, []string{"fast", "slow"}, names, "zero-duration predecessor must stay in the chain")
}

func TestWriteCriticalPath_DegradesWithoutEdges(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteCriticalPath(&buf, model.Trace{Spans: []model.Span{{SpanID: "r", Name: "x"}}}))
	assert.Contains(t, buf.String(), "no dependency edges")
}

func TestWriteCriticalPath_RendersHeaderAndPath(t *testing.T) {
	tr := model.Trace{
		DurationNs: 400,
		Spans: []model.Span{
			unitSpan(0, "A", 0, 100, 1),
			unitSpan(1, "B", 100, 300),
		},
	}
	var buf bytes.Buffer
	require.NoError(t, WriteCriticalPath(&buf, tr))
	out := buf.String()
	assert.Contains(t, out, "critical path")
	assert.Contains(t, out, "A")
	assert.Contains(t, out, "B")
}
