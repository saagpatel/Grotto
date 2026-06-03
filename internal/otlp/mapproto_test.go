package otlp

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/saagpatel/grotto/internal/model"
)

func TestMapExportRequest_ThreeSpans(t *testing.T) {
	traces := MapExportRequest(threeSpanRequest())
	require.Len(t, traces, 1)

	tr := traces[0]
	assert.Equal(t, "otlp", tr.Source)
	assert.Equal(t, fxTraceIDHex, tr.TraceID)
	assert.Equal(t, "build-svc", tr.RunLabel)
	assert.Equal(t, "build", tr.RootName)
	assert.Equal(t, 3, tr.SpanCount)
	assert.Equal(t, int64(0), tr.StartedNs, "min start across spans")
	assert.Equal(t, int64(600), tr.EndedNs, "max end across spans")

	byID := make(map[string]model.Span, len(tr.Spans))
	for _, s := range tr.Spans {
		byID[s.SpanID] = s
	}

	root := byID[hex.EncodeToString(fxRootID)]
	assert.Empty(t, root.ParentSpanID, "root has no parent")
	assert.Equal(t, model.KindInternal, root.Kind)
	assert.Equal(t, fxTraceIDHex, root.TraceID)
	require.Len(t, root.Attributes, 1)
	assert.Equal(t, model.Attribute{Key: "command", ValueType: "str", Value: "make all"}, root.Attributes[0])

	compile := byID[hex.EncodeToString(fxAID)]
	assert.Equal(t, hex.EncodeToString(fxRootID), compile.ParentSpanID)
	assert.Equal(t, model.Attribute{Key: "jobs", ValueType: "int", Value: "8"}, compile.Attributes[0])

	link := byID[hex.EncodeToString(fxBID)]
	assert.Equal(t, hex.EncodeToString(fxRootID), link.ParentSpanID)
	assert.Equal(t, model.StatusError, link.Status, "error status preserved")
	assert.Equal(t, model.Attribute{Key: "cached", ValueType: "bool", Value: "false"}, link.Attributes[0])
}

func TestMapExportRequest_GroupsByTraceID(t *testing.T) {
	t1 := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	t2 := []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	req := pbRequest("svc",
		pbSpan(t1, []byte("aaaaaaaa"), nil, "a", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 0, 10),
		pbSpan(t2, []byte("bbbbbbbb"), nil, "b", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 0, 10),
		pbSpan(t1, []byte("cccccccc"), []byte("aaaaaaaa"), "c", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 1, 9),
	)
	traces := MapExportRequest(req)
	require.Len(t, traces, 2, "one trace per distinct trace ID")
	assert.Equal(t, hex.EncodeToString(t1), traces[0].TraceID, "first-seen order preserved")
	assert.Equal(t, 2, traces[0].SpanCount)
	assert.Equal(t, 1, traces[1].SpanCount)
}

func TestMapAttribute_Types(t *testing.T) {
	double := &commonpb.KeyValue{Key: "ratio", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}}
	assert.Equal(t, model.Attribute{Key: "ratio", ValueType: "float", Value: "1.5"}, mapAttribute(double))

	empty := &commonpb.KeyValue{Key: "missing"}
	got := mapAttribute(empty)
	assert.Equal(t, "missing", got.Key)
	assert.Equal(t, "str", got.ValueType, "an empty value falls back to str")
}

func TestMapExportRequest_Empty(t *testing.T) {
	assert.Empty(t, MapExportRequest(nil))
}
