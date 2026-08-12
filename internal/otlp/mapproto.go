// Package otlp implements Grotto's OTLP receiver: a loopback gRPC (:4317) and
// HTTP (:4318) server that accepts OpenTelemetry trace exports, maps the
// protobuf spans onto Grotto's shared model.Span shape, and feeds them through a
// single-writer sink into the same SQLite store the marks path uses.
package otlp

import (
	"encoding/hex"
	"math"
	"strconv"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/saagpatel/grotto/internal/model"
)

const serviceNameKey = "service.name"

// MapExportRequest converts an OTLP export request into one model.Trace per
// distinct trace ID, preserving first-seen order. Spans are mapped individually
// and grouped; each resulting trace carries source "otlp" and a run label taken
// from the originating resource's service.name (falling back to the root span
// name).
func MapExportRequest(req *coltracepb.ExportTraceServiceRequest) []model.Trace {
	type traceAccum struct {
		spans []model.Span
		label string
	}
	byTrace := make(map[string]*traceAccum)
	var order []string

	for _, rs := range req.GetResourceSpans() {
		label := serviceName(rs.GetResource())
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				ms := mapSpan(sp)
				a, ok := byTrace[ms.TraceID]
				if !ok {
					a = &traceAccum{label: label}
					byTrace[ms.TraceID] = a
					order = append(order, ms.TraceID)
				}
				a.spans = append(a.spans, ms)
			}
		}
	}

	traces := make([]model.Trace, 0, len(order))
	for _, tid := range order {
		traces = append(traces, buildTrace(tid, byTrace[tid].spans, byTrace[tid].label))
	}
	return traces
}

// mapSpan converts one protobuf span to a model.Span. Trace/span IDs are
// lowercase hex; an empty parent ID marks a root span.
func mapSpan(sp *tracepb.Span) model.Span {
	status := model.StatusUnset
	if s := sp.GetStatus(); s != nil {
		status = model.StatusCode(s.GetCode())
	}

	ms := model.Span{
		SpanID:  hex.EncodeToString(sp.GetSpanId()),
		TraceID: hex.EncodeToString(sp.GetTraceId()),
		// GetParentSpanId returns nil (not zero bytes) when absent, and
		// hex.EncodeToString(nil) == "", which is the root sentinel.
		ParentSpanID: hex.EncodeToString(sp.GetParentSpanId()),
		Name:         sp.GetName(),
		Kind:         model.SpanKind(sp.GetKind()),
		Status:       status,
		StartedNs:    int64(sp.GetStartTimeUnixNano()),
		EndedNs:      int64(sp.GetEndTimeUnixNano()),
	}
	ms.DurationNs = ms.EndedNs - ms.StartedNs

	if attrs := sp.GetAttributes(); len(attrs) > 0 {
		ms.Attributes = make([]model.Attribute, 0, len(attrs))
		for _, kv := range attrs {
			ms.Attributes = append(ms.Attributes, mapAttribute(kv))
		}
	}
	return ms
}

// mapAttribute flattens an OTLP key/value into Grotto's stringified attribute,
// recording the original scalar type. Non-scalar values (arrays, maps) are
// rendered best-effort as strings.
func mapAttribute(kv *commonpb.KeyValue) model.Attribute {
	a := model.Attribute{Key: kv.GetKey(), ValueType: "str"}
	switch v := kv.GetValue().GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		a.ValueType, a.Value = "str", v.StringValue
	case *commonpb.AnyValue_BoolValue:
		a.ValueType, a.Value = "bool", strconv.FormatBool(v.BoolValue)
	case *commonpb.AnyValue_IntValue:
		a.ValueType, a.Value = "int", strconv.FormatInt(v.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		a.ValueType, a.Value = "float", strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		a.ValueType, a.Value = "bytes", hex.EncodeToString(v.BytesValue)
	default:
		a.Value = kv.GetValue().String() // array/kvlist/empty: best-effort
	}
	return a
}

// buildTrace derives a trace's run metadata from its spans: time bounds from the
// min start / max end, root from the first parentless span.
func buildTrace(traceID string, spans []model.Span, label string) model.Trace {
	var (
		startNs  = int64(math.MaxInt64)
		endNs    int64
		rootName string
	)
	for _, s := range spans {
		if s.StartedNs < startNs {
			startNs = s.StartedNs
		}
		if s.EndedNs > endNs {
			endNs = s.EndedNs
		}
		if s.ParentSpanID == "" && rootName == "" {
			rootName = s.Name
		}
	}
	if len(spans) == 0 {
		startNs = 0
	}
	if rootName == "" && len(spans) > 0 {
		rootName = spans[0].Name
	}
	// Build a label that distinguishes traces from the same service. The
	// service.name alone repeats across every request, so append the root span
	// name (e.g. "checkout-api · POST /checkout"). Fall back to the root name
	// when there is no service.name, and to the bare service name when it equals
	// the root (no value in doubling it).
	switch {
	case label == "":
		label = rootName
	case rootName != "" && rootName != label:
		label = label + " · " + rootName
	}

	return model.Trace{
		TraceID:    traceID,
		RunLabel:   label,
		Source:     "otlp",
		RootName:   rootName,
		StartedNs:  startNs,
		EndedNs:    endNs,
		DurationNs: endNs - startNs,
		SpanCount:  len(spans),
		Spans:      spans,
	}
}

// serviceName extracts the service.name resource attribute, or "" if absent.
func serviceName(r *resourcepb.Resource) string {
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == serviceNameKey {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}
