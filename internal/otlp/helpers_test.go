package otlp

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/saagpatel/grotto/internal/store"
)

// Fixed IDs for the three-span fixture (16-byte trace, 8-byte spans).
var (
	fxTraceID = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	fxRootID  = []byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7}
	fxAID     = []byte{0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7}
	fxBID     = []byte{0xc0, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7}

	fxTraceIDHex = hex.EncodeToString(fxTraceID)
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func boolAttr(k string, v bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}}
}

func pbSpan(traceID, spanID, parentID []byte, name string, kind tracepb.Span_SpanKind, code tracepb.Status_StatusCode, start, end uint64, attrs ...*commonpb.KeyValue) *tracepb.Span {
	return &tracepb.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		ParentSpanId:      parentID,
		Name:              name,
		Kind:              kind,
		Status:            &tracepb.Status{Code: code},
		StartTimeUnixNano: start,
		EndTimeUnixNano:   end,
		Attributes:        attrs,
	}
}

func pbRequest(service string, spans ...*tracepb.Span) *coltracepb.ExportTraceServiceRequest {
	var res *resourcepb.Resource
	if service != "" {
		res = &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", service)}}
	}
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource:   res,
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}},
	}
}

// threeSpanRequest is the canonical fixture: one trace, root + two children,
// each with one typed attribute, the second child in error status.
func threeSpanRequest() *coltracepb.ExportTraceServiceRequest {
	return pbRequest("build-svc",
		pbSpan(fxTraceID, fxRootID, nil, "build", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 0, 600, strAttr("command", "make all")),
		pbSpan(fxTraceID, fxAID, fxRootID, "compile", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 10, 200, intAttr("jobs", 8)),
		pbSpan(fxTraceID, fxBID, fxRootID, "link", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_ERROR, 210, 400, boolAttr("cached", false)),
	)
}

// bigTraceID and bigRequest build a single trace of n spans (1 root + n-1
// children) for the high-volume storage test.
var bigTraceID = []byte{0xde, 0xad, 0xbe, 0xef, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}

func bigTraceIDHex() string { return hex.EncodeToString(bigTraceID) }

func bigRequest(n int) *coltracepb.ExportTraceServiceRequest {
	spans := make([]*tracepb.Span, 0, n)
	root := []byte("rootroot")
	spans = append(spans, pbSpan(bigTraceID, root, nil, "root", tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, 0, uint64(n*10)))
	for i := 1; i < n; i++ {
		sid := []byte(fmt.Sprintf("span%04d", i))
		spans = append(spans, pbSpan(bigTraceID, sid, root, fmt.Sprintf("child-%d", i),
			tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Status_STATUS_CODE_OK, uint64(i*10), uint64(i*10+5)))
	}
	return pbRequest("bulk-svc", spans...)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}
