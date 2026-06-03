package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

const ms = int64(1_000_000)

// renderFixtureTrace is a deterministic 6-span trace (1 root + 5 descendants)
// shared by the layout and waterfall golden tests:
//
//	build               0..600ms
//	├── compile         0..200ms
//	│   ├── parse       0..90ms
//	│   └── codegen     100..190ms
//	├── link            200..400ms
//	└── strip           410..590ms
func renderFixtureTrace() model.Trace {
	span := func(id, parent, name string, start, end int64) model.Span {
		return model.Span{
			SpanID: id, TraceID: "t", ParentSpanID: parent, Name: name,
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: start * ms, EndedNs: end * ms, DurationNs: (end - start) * ms,
		}
	}
	spans := []model.Span{
		span("build", "", "build", 0, 600),
		span("compile", "build", "compile", 0, 200),
		span("parse", "compile", "parse", 0, 90),
		span("codegen", "compile", "codegen", 100, 190),
		span("link", "build", "link", 200, 400),
		span("strip", "build", "strip", 410, 590),
	}
	return model.Trace{
		TraceID: "t", RunLabel: "build", Source: "mark", RootName: "build",
		StartedNs: 0, EndedNs: 600 * ms, DurationNs: 600 * ms,
		SpanCount: len(spans), Spans: spans,
	}
}

func TestLayout_OffsetsAndWidths(t *testing.T) {
	root := model.AssembleTree(renderFixtureTrace().Spans)
	require.NotNil(t, root)

	bars := Layout(root, 40)
	require.Len(t, bars, 6)

	byName := make(map[string]Bar, len(bars))
	for _, b := range bars {
		byName[b.Name] = b
	}

	expected := map[string]struct{ offset, width int }{
		"build":   {0, 40},
		"compile": {0, 13},
		"parse":   {0, 6},
		"codegen": {7, 6},
		"link":    {13, 13},
		"strip":   {27, 12},
	}
	const tol = 1.0
	for name, w := range expected {
		b := byName[name]
		assert.InDeltaf(t, w.offset, b.Offset, tol, "%s offset", name)
		assert.InDeltaf(t, w.width, b.Width, tol, "%s width", name)
	}
}

func TestLayout_PreOrder(t *testing.T) {
	root := model.AssembleTree(renderFixtureTrace().Spans)
	bars := Layout(root, 40)

	names := make([]string, 0, len(bars))
	for _, b := range bars {
		names = append(names, b.Name)
	}
	assert.Equal(t, []string{"build", "compile", "parse", "codegen", "link", "strip"}, names)
}

func TestLayout_NilAndZeroWidth(t *testing.T) {
	assert.Nil(t, Layout(nil, 40))
	root := model.AssembleTree(renderFixtureTrace().Spans)
	assert.Nil(t, Layout(root, 0))
}
