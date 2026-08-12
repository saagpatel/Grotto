package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/saagpatel/grotto/internal/ledger"
	"github.com/saagpatel/grotto/internal/otlp"
)

func TestSyntheticOTLPSpanProducesReconciledLedger(t *testing.T) {
	traceID := []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	request := &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: traceID, SpanId: spanID, Name: "chat gpt-fixture",
			Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: 100, EndTimeUnixNano: 200,
			Attributes: []*commonpb.KeyValue{
				stringKV("gen_ai.provider.name", "openai"),
				stringKV("gen_ai.response.model", "gpt-fixture"),
				intKV("gen_ai.usage.input_tokens", 8),
				intKV("gen_ai.usage.output_tokens", 3),
				intKV("gen_ai.usage.cache_read.input_tokens", 2),
				intKV("gen_ai.usage.cache_creation.input_tokens", 0),
				intKV("gen_ai.usage.reasoning.output_tokens", 1),
				intKV("grotto.usage.tool.input_tokens", 0),
				intKV("grotto.context.window.limit_tokens", 32),
			},
		}}}},
	}}}

	traces := otlp.MapExportRequest(request)
	require.Len(t, traces, 1)
	report := ledger.Build(traces[0])
	require.NoError(t, ledger.ValidateReport(report))
	require.Empty(t, report.Issues)
	require.NotNil(t, report.Summary.Usage.Total.Value)
	assert.Equal(t, int64(11), *report.Summary.Usage.Total.Value)
	require.NotNil(t, report.Summary.Context.Ratio)
	assert.InDelta(t, 11.0/32.0, *report.Summary.Context.Ratio, 0.000001)

	inputSources := report.Rows[0].Observed.Input.Sources
	require.Len(t, inputSources, 1)
	assert.Equal(t, "gen_ai.usage.input_tokens", inputSources[0].Attribute)
	assert.Equal(t, "8", inputSources[0].Value)
}

func stringKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func intKV(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}
