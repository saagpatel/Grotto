package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpanKindJSONLabels(t *testing.T) {
	cases := map[SpanKind]string{
		KindUnspecified: `"unspecified"`,
		KindInternal:    `"internal"`,
		KindServer:      `"server"`,
		KindClient:      `"client"`,
		KindProducer:    `"producer"`,
		KindConsumer:    `"consumer"`,
		SpanKind(99):    `"unspecified"`,
	}
	for kind, want := range cases {
		got, err := json.Marshal(kind)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
}

func TestStatusCodeJSONLabels(t *testing.T) {
	cases := map[StatusCode]string{
		StatusUnset:    `"unset"`,
		StatusOk:       `"ok"`,
		StatusError:    `"error"`,
		StatusCode(99): `"unset"`,
	}
	for status, want := range cases {
		got, err := json.Marshal(status)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
}

func TestSpanEnumsUnmarshalReadableAndNumericForms(t *testing.T) {
	var kind SpanKind
	require.NoError(t, json.Unmarshal([]byte(`"client"`), &kind))
	assert.Equal(t, KindClient, kind)
	require.NoError(t, json.Unmarshal([]byte(`4`), &kind))
	assert.Equal(t, KindProducer, kind)

	var status StatusCode
	require.NoError(t, json.Unmarshal([]byte(`"error"`), &status))
	assert.Equal(t, StatusError, status)
	require.NoError(t, json.Unmarshal([]byte(`1`), &status))
	assert.Equal(t, StatusOk, status)

	assert.Error(t, json.Unmarshal([]byte(`"mystery"`), &kind))
	assert.Error(t, json.Unmarshal([]byte(`99`), &status))
}

// sixSpanFixture returns a 6-span trace shaped as:
//
//	root
//	├── a
//	│   ├── a1
//	│   └── a2
//	└── b
//	    └── b1
//
// The slice is intentionally unsorted to exercise deterministic child ordering.
func sixSpanFixture() []Span {
	return []Span{
		{SpanID: "b1", ParentSpanID: "b", Name: "b1", StartedNs: 60},
		{SpanID: "a", ParentSpanID: "root", Name: "a", StartedNs: 10},
		{SpanID: "root", ParentSpanID: "", Name: "root", StartedNs: 0},
		{SpanID: "a2", ParentSpanID: "a", Name: "a2", StartedNs: 30},
		{SpanID: "b", ParentSpanID: "root", Name: "b", StartedNs: 50},
		{SpanID: "a1", ParentSpanID: "a", Name: "a1", StartedNs: 20},
	}
}

func TestAssembleTree_NestingAndDepth(t *testing.T) {
	root := AssembleTree(sixSpanFixture())
	require.NotNil(t, root)
	assert.Equal(t, "root", root.Span.Name)
	assert.Equal(t, 0, root.Depth)

	// Children ordered by StartedNs: a (10) before b (50).
	require.Len(t, root.Children, 2)
	a, b := root.Children[0], root.Children[1]
	assert.Equal(t, "a", a.Span.Name)
	assert.Equal(t, "b", b.Span.Name)
	assert.Equal(t, 1, a.Depth)
	assert.Equal(t, 1, b.Depth)

	// a's children ordered a1 (20) before a2 (30), both at depth 2.
	require.Len(t, a.Children, 2)
	assert.Equal(t, "a1", a.Children[0].Span.Name)
	assert.Equal(t, "a2", a.Children[1].Span.Name)
	assert.Equal(t, 2, a.Children[0].Depth)
	assert.Equal(t, 2, a.Children[1].Depth)

	require.Len(t, b.Children, 1)
	assert.Equal(t, "b1", b.Children[0].Span.Name)
	assert.Equal(t, 2, b.Children[0].Depth)
}

func TestAssembleTree_Empty(t *testing.T) {
	assert.Nil(t, AssembleTree(nil))
	assert.Nil(t, AssembleTree([]Span{}))
}

func TestAssembleTree_OrphanParentSkipped(t *testing.T) {
	spans := []Span{
		{SpanID: "root", ParentSpanID: "", Name: "root"},
		{SpanID: "orphan", ParentSpanID: "ghost", Name: "orphan"},
	}
	root := AssembleTree(spans)
	require.NotNil(t, root)
	assert.Empty(t, root.Children, "span with a missing parent must not attach to the root")
}

func TestAssembleTree_NoRootReturnsNil(t *testing.T) {
	spans := []Span{
		{SpanID: "a", ParentSpanID: "b", Name: "a"},
		{SpanID: "b", ParentSpanID: "a", Name: "b"},
	}
	assert.Nil(t, AssembleTree(spans), "input with no root span yields nil")
}

func TestAssembleTree_DuplicateSpanIDFirstWins(t *testing.T) {
	spans := []Span{
		{SpanID: "root", ParentSpanID: "", Name: "root"},
		{SpanID: "a", ParentSpanID: "root", Name: "a-first"},
		{SpanID: "a", ParentSpanID: "root", Name: "a-duplicate"},
	}
	root := AssembleTree(spans)
	require.NotNil(t, root)
	require.Len(t, root.Children, 1, "a duplicate span ID must not produce a second child")
	assert.Equal(t, "a-first", root.Children[0].Span.Name)
}

func TestAssembleTree_MultipleRootsFirstWins(t *testing.T) {
	spans := []Span{
		{SpanID: "r1", ParentSpanID: "", Name: "r1", StartedNs: 0},
		{SpanID: "c1", ParentSpanID: "r1", Name: "c1", StartedNs: 5},
		{SpanID: "r2", ParentSpanID: "", Name: "r2", StartedNs: 10},
	}
	root := AssembleTree(spans)
	require.NotNil(t, root)
	assert.Equal(t, "r1", root.Span.Name, "the first parentless span is the root")
	require.Len(t, root.Children, 1)
	assert.Equal(t, "c1", root.Children[0].Span.Name)
}

func TestAssembleTree_SelfParentDoesNotRecurse(t *testing.T) {
	// A self-parented span must not be attached to itself (which would loop).
	spans := []Span{
		{SpanID: "root", ParentSpanID: "", Name: "root"},
		{SpanID: "x", ParentSpanID: "x", Name: "x"},
	}
	root := AssembleTree(spans)
	require.NotNil(t, root)
	assert.Empty(t, root.Children)
}
