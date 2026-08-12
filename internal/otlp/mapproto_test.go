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
	assert.Equal(t, "build-svc · build", tr.RunLabel, "label combines service.name and root span name to distinguish same-service traces")
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

	binary := &commonpb.KeyValue{Key: "payload", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{0, 1, 255}}}}
	assert.Equal(t, model.Attribute{Key: "payload", ValueType: "bytes", Value: "0001ff"}, mapAttribute(binary))
}

func TestMapExportRequest_Empty(t *testing.T) {
	assert.Empty(t, MapExportRequest(nil))
}

func TestMapExportRequest_PreservesLinksAndDroppedCounts(t *testing.T) {
	root := pbSpan(fxTraceID, fxRootID, nil, "root", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 0, 10)
	child := pbSpan(fxTraceID, fxAID, fxRootID, "child", tracepb.Span_SPAN_KIND_CLIENT, tracepb.Status_STATUS_CODE_OK, 1, 9)
	child.DroppedAttributesCount = 2
	child.DroppedLinksCount = 3
	child.Links = []*tracepb.Span_Link{{
		TraceId: fxTraceID, SpanId: fxBID, TraceState: "vendor=test", Flags: 1,
		DroppedAttributesCount: 4,
		Attributes:             []*commonpb.KeyValue{strAttr("gen_ai.response.id", "resp_prior")},
	}}

	traces := MapExportRequest(pbRequest("svc", root, child))
	require.Len(t, traces, 1)
	require.Len(t, traces[0].Spans, 2)
	got := traces[0].Spans[1]
	assert.Equal(t, uint32(2), got.DroppedAttributesCount)
	assert.Equal(t, uint32(3), got.DroppedLinksCount)
	require.Len(t, got.Links, 1)
	assert.Equal(t, hex.EncodeToString(fxBID), got.Links[0].SpanID)
	assert.Equal(t, "vendor=test", got.Links[0].TraceState)
	assert.Equal(t, uint32(4), got.Links[0].DroppedAttributesCount)
	assert.Equal(t, uint32(1), got.Links[0].Flags)
	assert.Equal(t, model.Attribute{Key: "gen_ai.response.id", ValueType: "str", Value: "resp_prior"}, got.Links[0].Attributes[0])
}

func TestBuildTrace_Label(t *testing.T) {
	root := model.Span{SpanID: "r", Name: "POST /checkout", EndedNs: 10}
	tests := []struct {
		name  string
		label string // the service.name passed in
		want  string
	}{
		{"service and root combine", "checkout-api", "checkout-api · POST /checkout"},
		{"no service falls back to root name", "", "POST /checkout"},
		{"service equal to root is not doubled", "POST /checkout", "POST /checkout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := buildTrace("tid", []model.Span{root}, tt.label)
			assert.Equal(t, tt.want, tr.RunLabel)
		})
	}
}
