package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

// diffTrace builds a trace: root → {compile, link}, where compile/link durations
// are caller-supplied so two runs can be compared.
func diffTrace(label string, compileNs, linkNs int64, withLink bool) model.Trace {
	spans := []model.Span{
		{SpanID: "root", Name: "build", StartedNs: 0, EndedNs: 1000, DurationNs: 1000},
		{SpanID: "c", ParentSpanID: "root", Name: "compile", StartedNs: 10, EndedNs: 10 + compileNs, DurationNs: compileNs},
	}
	if withLink {
		spans = append(spans, model.Span{
			SpanID: "l", ParentSpanID: "root", Name: "link",
			StartedNs: 500, EndedNs: 500 + linkNs, DurationNs: linkNs,
		})
	}
	return model.Trace{TraceID: label, RunLabel: label, RootName: "build", DurationNs: 1000, SpanCount: len(spans), Spans: spans}
}

func deltaByName(deltas []SpanDelta, name string) (SpanDelta, bool) {
	for _, d := range deltas {
		if d.Name == name {
			return d, true
		}
	}
	return SpanDelta{}, false
}

func TestDiff_MatchedSpansReportDelta(t *testing.T) {
	a := diffTrace("a", 100, 200, true)
	b := diffTrace("b", 150, 180, true)

	deltas := Diff(a, b)

	compile, ok := deltaByName(deltas, "compile")
	require.True(t, ok)
	assert.Equal(t, DeltaMatched, compile.Kind)
	assert.Equal(t, int64(50), compile.DeltaNs, "compile got 50ns slower (100→150)")

	link, ok := deltaByName(deltas, "link")
	require.True(t, ok)
	assert.Equal(t, int64(-20), link.DeltaNs, "link got 20ns faster (200→180)")
}

func TestDiff_AddedAndRemovedSpans(t *testing.T) {
	a := diffTrace("a", 100, 200, true) // has link
	b := diffTrace("b", 100, 0, false)  // no link

	deltas := Diff(a, b)

	link, ok := deltaByName(deltas, "link")
	require.True(t, ok)
	assert.Equal(t, DeltaRemoved, link.Kind, "link present in A but not B → removed")
	assert.Equal(t, int64(200), link.ANs)

	// Reverse: link is added when going from no-link to link.
	rev := Diff(b, a)
	linkRev, ok := deltaByName(rev, "link")
	require.True(t, ok)
	assert.Equal(t, DeltaAdded, linkRev.Kind, "link absent in B but present in A → added")
	assert.Equal(t, int64(200), linkRev.BNs)
}

func TestDiff_EmptyTraces(t *testing.T) {
	assert.Nil(t, Diff(model.Trace{}, model.Trace{}))
}

func TestWriteDiff_RendersHeaderAndSignedDeltas(t *testing.T) {
	a := diffTrace("run-a", 100, 200, true)
	b := diffTrace("run-b", 150, 180, true)

	var buf bytes.Buffer
	require.NoError(t, WriteDiff(&buf, a, b, Diff(a, b)))
	out := buf.String()

	assert.Contains(t, out, "diff  run-a", "header leads with the A trace id")
	assert.Contains(t, out, "→ run-b", "header shows the B trace id after the arrow")
	assert.Contains(t, out, "compile")
	assert.Contains(t, out, "+50ns", "matched slower span shows a signed positive delta")
	assert.Contains(t, out, "-20ns", "matched faster span shows a signed negative delta")
}

// TestWriteDiff_HeaderDistinguishesPrefixIdenticalLabels guards the regression
// where truncating both labels to a fixed width made two traces whose commands
// share a long prefix render identically: the short trace IDs keep them apart.
func TestWriteDiff_HeaderDistinguishesPrefixIdenticalLabels(t *testing.T) {
	longCmd := "bash -c grotto mark build CGO_ENABLED=0 go build ./... grotto mark test"
	a := diffTrace(longCmd, 100, 200, true)
	a.TraceID = "6e87e1ffbb898f0f5c4aa2bb4f7ad67a"
	b := diffTrace(longCmd, 150, 180, true)
	b.TraceID = "551afd0d19e1dd3c650d7539bbc6c5bb"

	var buf bytes.Buffer
	require.NoError(t, WriteDiff(&buf, a, b, Diff(a, b)))
	header := strings.SplitN(buf.String(), "\n", 2)[0]

	assert.Contains(t, header, "6e87e1ff", "header carries the A short trace id")
	assert.Contains(t, header, "551afd0d", "header carries the B short trace id")
	assert.NotContains(t, header, a.TraceID, "header uses the short id, not the full 32-char key")
}

func TestSignedDuration(t *testing.T) {
	assert.Equal(t, "+90ms", signedDuration(90*ms))
	assert.Equal(t, "-90ms", signedDuration(-90*ms))
	assert.Equal(t, "0", signedDuration(0))
}
