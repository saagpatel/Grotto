package compaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	openaiadapter "github.com/saagpatel/grotto/internal/compaction/openai"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

const fixtureTraceID = "11111111111111111111111111111111"

func strAttr(key, value string) model.Attribute {
	return model.Attribute{Key: key, ValueType: "str", Value: value}
}

func responseSpan(id string, started int64, responseID, previousID string, input, output *int64, compacted *bool) model.Span {
	attrs := []model.Attribute{strAttr(attrOperation, "chat"), strAttr(attrProvider, "openai")}
	if responseID != "" {
		attrs = append(attrs, strAttr(attrResponseID, responseID))
	}
	if previousID != "" {
		attrs = append(attrs, strAttr(attrPreviousResponse, previousID))
	}
	if input != nil {
		attrs = append(attrs, model.Attribute{Key: attrInputTokens, ValueType: "int", Value: strconv64(*input)})
	}
	if output != nil {
		attrs = append(attrs, model.Attribute{Key: attrOutputTokens, ValueType: "int", Value: strconv64(*output)})
	}
	if compacted != nil {
		attrs = append(attrs, model.Attribute{Key: attrCompacted, ValueType: "bool", Value: strconvBool(*compacted)})
	}
	return model.Span{SpanID: id, TraceID: fixtureTraceID, ParentSpanID: "root", Name: "chat model", StartedNs: started, EndedNs: started + 10, DurationNs: 10, Attributes: attrs}
}

func strconv64(value int64) string  { return fmt.Sprintf("%d", value) }
func strconvBool(value bool) string { return fmt.Sprintf("%t", value) }

func trace(spans ...model.Span) model.Trace {
	root := model.Span{SpanID: "root", TraceID: fixtureTraceID, Name: "synthetic conversation", StartedNs: 0, EndedNs: 1000, DurationNs: 1000}
	all := append([]model.Span{root}, spans...)
	return model.Trace{TraceID: fixtureTraceID, Source: "otlp", SpanCount: len(all), Spans: all}
}

func TestAnalyze_CompactionFixtureMatrix(t *testing.T) {
	t.Run("no compaction does not invent a boundary", func(t *testing.T) {
		in1, out1, in2, out2 := int64(100), int64(10), int64(120), int64(12)
		report := Analyze(trace(
			responseSpan("a", 1, "resp_a", "", &in1, &out1, nil),
			responseSpan("b", 2, "resp_b", "resp_a", &in2, &out2, nil),
		))
		assert.Equal(t, "unknown", report.Observations[1].Compaction.State)
		assert.Equal(t, "unknown", report.Observations[1].ContextReset.State)
	})

	t.Run("one and repeated compactions show local deltas and supplied drift", func(t *testing.T) {
		before, after, repeated := int64(10000), int64(3500), int64(1800)
		output := int64(500)
		yes := true
		a := responseSpan("a", 100, "resp_a", "", &before, &output, nil)
		b := responseSpan("b", 200, "resp_b", "resp_a", &after, &output, &yes)
		c := responseSpan("c", 300, "resp_c", "resp_b", &repeated, &output, &yes)
		a.Attributes = append(a.Attributes, strAttr(attrAnswerHash, "hash-a"))
		b.Attributes = append(b.Attributes, strAttr(attrAnswerHash, "hash-b"))
		c.Attributes = append(c.Attributes, strAttr(attrAnswerHash, "hash-b"))
		report := Analyze(trace(c, a, b)) // deliberately out of order

		assert.Equal(t, []string{"resp_a", "resp_b", "resp_c"}, []string{
			report.Observations[0].Chain.ResponseID,
			report.Observations[1].Chain.ResponseID,
			report.Observations[2].Chain.ResponseID,
		})
		assert.Equal(t, int64(-6500), *report.Observations[1].Tokens.InputShift.Delta.Value)
		assert.Equal(t, "detected", report.Observations[1].ContextReset.State)
		assert.Equal(t, "changed", report.Observations[1].AnswerDrift.State)
		assert.Equal(t, "unchanged", report.Observations[2].AnswerDrift.State)
	})

	t.Run("missing previous response remains broken ancestry", func(t *testing.T) {
		report := Analyze(trace(responseSpan("b", 2, "resp_b", "resp_missing", nil, nil, nil)))
		assert.Equal(t, "missing_ancestry", report.Observations[0].Chain.Status)
		assertWarning(t, report, "missing_previous_response")
	})

	t.Run("branched chain is explicit", func(t *testing.T) {
		a := responseSpan("a", 1, "resp_a", "", nil, nil, nil)
		b := responseSpan("b", 2, "resp_b", "resp_a", nil, nil, nil)
		c := responseSpan("c", 3, "resp_c", "resp_a", nil, nil, nil)
		report := Analyze(trace(a, b, c))
		assert.Equal(t, "branched", report.Observations[1].Chain.Status)
		assert.Equal(t, "branched", report.Observations[2].Chain.Status)
	})

	t.Run("missing tokens keep reset unknown", func(t *testing.T) {
		yes := true
		a := responseSpan("a", 1, "resp_a", "", nil, nil, nil)
		b := responseSpan("b", 2, "resp_b", "resp_a", nil, nil, &yes)
		report := Analyze(trace(a, b))
		assert.Equal(t, "unknown", report.Observations[1].Tokens.Input.State)
		assert.Equal(t, "unknown", report.Observations[1].ContextReset.State)
	})

	t.Run("truncated attributes and links are surfaced", func(t *testing.T) {
		span := responseSpan("a", 1, "resp_a", "", nil, nil, nil)
		span.DroppedAttributesCount = 2
		span.DroppedLinksCount = 1
		span.Links = []model.SpanLink{{TraceID: fixtureTraceID, SpanID: "missing", DroppedAttributesCount: 3}}
		report := Analyze(trace(span))
		assertWarning(t, report, "truncated_attributes")
		assertWarning(t, report, "truncated_links")
		assertWarning(t, report, "truncated_link_attributes")
	})

	t.Run("provider extension is isolated and experimental", func(t *testing.T) {
		span := model.Span{SpanID: "p", TraceID: fixtureTraceID, StartedNs: 1, Attributes: []model.Attribute{
			strAttr(openaiadapter.AttrResponseID, "resp_provider"),
			strAttr(openaiadapter.AttrPreviousResponse, "resp_prior"),
			strAttr(openaiadapter.AttrOutputItemType, "compaction"),
			strAttr(openaiadapter.AttrContextType, "compaction"),
		}}
		report := Analyze(trace(span))
		assert.Equal(t, "detected", report.Observations[0].Compaction.State)
		assert.True(t, report.Observations[0].Compaction.Armed)
		assert.True(t, report.Observations[0].Provenance[0].Experimental)
	})
}

func TestAnalyze_SpanLinksSupplyAndValidateAncestry(t *testing.T) {
	a := responseSpan("a", 1, "resp_a", "", nil, nil, nil)
	b := responseSpan("b", 2, "resp_b", "", nil, nil, nil)
	b.Links = []model.SpanLink{{TraceID: fixtureTraceID, SpanID: "a"}}
	report := Analyze(trace(a, b))
	assert.Equal(t, "resp_a", report.Observations[1].Chain.PreviousResponseID)
	assert.Equal(t, "linked", report.Observations[1].Chain.Status)
	assert.Equal(t, "a", report.Observations[1].Chain.LinkedSpanID)

	c := responseSpan("c", 3, "resp_c", "resp_a", nil, nil, nil)
	c.Links = []model.SpanLink{{TraceID: fixtureTraceID, SpanID: "b"}}
	conflict := Analyze(trace(a, b, c))
	assert.Equal(t, "conflict", conflict.Observations[2].Chain.Status)
	assertWarning(t, conflict, "link_attribute_conflict")

	external := responseSpan("external", 4, "resp_external", "", nil, nil, nil)
	external.Links = []model.SpanLink{{
		TraceID: "22222222222222222222222222222222", SpanID: "prior-span",
		Attributes: []model.Attribute{strAttr(attrResponseID, "resp_prior_trace")},
	}}
	externalReport := Analyze(trace(external))
	assert.Equal(t, "resp_prior_trace", externalReport.Observations[0].Chain.PreviousResponseID)
	assert.Equal(t, "linked_external", externalReport.Observations[0].Chain.Status)
	assert.Equal(t, "prior-span", externalReport.Observations[0].Chain.LinkedSpanID)
}

func TestAnalyze_OrdinaryCrossTraceLinkDoesNotSupplyResponseAncestry(t *testing.T) {
	span := responseSpan("external", 1, "resp_current", "resp_missing", nil, nil, nil)
	span.Links = []model.SpanLink{{
		TraceID: "22222222222222222222222222222222", SpanID: "ordinary-linked-span",
		Attributes: []model.Attribute{strAttr("messaging.message.id", "message-1")},
	}}

	report := Analyze(trace(span))
	require.Len(t, report.Observations, 1)
	assert.Equal(t, "missing_ancestry", report.Observations[0].Chain.Status)
	assert.Empty(t, report.Observations[0].Chain.LinkedSpanID)
	assertWarning(t, report, "missing_previous_response")
}

func TestAnalyze_NonGenAITraceEmitsEmptyObservationsArray(t *testing.T) {
	report := Analyze(trace())
	require.NotNil(t, report.Observations)
	assert.Empty(t, report.Observations)

	var encoded bytes.Buffer
	require.NoError(t, WriteJSON(&encoded, report))
	assert.Contains(t, encoded.String(), `"observations": []`)
}

func TestAnalyze_LinkDerivedAncestryOrdersParentBeforeClockSkewedChild(t *testing.T) {
	parent := responseSpan("z-parent", 20, "resp_parent", "", nil, nil, nil)
	child := responseSpan("a-child", 10, "resp_child", "", nil, nil, nil)
	child.Links = []model.SpanLink{{TraceID: fixtureTraceID, SpanID: parent.SpanID}}

	report := Analyze(trace(child, parent))
	require.Len(t, report.Observations, 2)
	assert.Equal(t, "z-parent", report.Observations[0].SpanID)
	assert.Equal(t, "a-child", report.Observations[1].SpanID)
	assert.Equal(t, "resp_parent", report.Observations[1].Chain.PreviousResponseID)
	assert.Equal(t, "linked", report.Observations[1].Chain.Status)
}

func TestAnalyze_DuplicateResponseIDKeepsDependentAncestryUnknown(t *testing.T) {
	beforeA, beforeB, after := int64(100), int64(200), int64(50)
	yes := true
	first := responseSpan("first", 1, "resp_duplicate", "", &beforeA, nil, nil)
	second := responseSpan("second", 2, "resp_duplicate", "", &beforeB, nil, nil)
	child := responseSpan("child", 3, "resp_child", "resp_duplicate", &after, nil, &yes)

	report := Analyze(trace(first, second, child))
	require.Len(t, report.Observations, 3)
	dependent := report.Observations[2]
	assert.Equal(t, "ambiguous", dependent.Chain.Status)
	assert.Equal(t, "unknown", dependent.Tokens.InputShift.Before.State)
	assert.Equal(t, "unknown", dependent.Tokens.InputShift.Delta.State)
	assert.Equal(t, "unknown", dependent.ContextReset.State)
	assertWarning(t, report, "duplicate_response_id")
	assertWarning(t, report, "ambiguous_previous_response")
}

func TestAnalyze_MissingSharedPreviousIDIsNotAFalseBranch(t *testing.T) {
	first := responseSpan("first", 1, "resp_first", "resp_missing", nil, nil, nil)
	second := responseSpan("second", 2, "resp_second", "resp_missing", nil, nil, nil)

	report := Analyze(trace(first, second))
	require.Len(t, report.Observations, 2)
	assert.Equal(t, "missing_ancestry", report.Observations[0].Chain.Status)
	assert.Equal(t, "missing_ancestry", report.Observations[1].Chain.Status)
	assertWarning(t, report, "missing_previous_response")
}

func TestAnalyze_MalformedDataIsDeterministicAndNonSemantic(t *testing.T) {
	no := false
	span := responseSpan("bad", 1, "resp_bad", "", nil, nil, &no)
	span.Attributes = append(span.Attributes,
		model.Attribute{Key: attrInputTokens, ValueType: "int", Value: "-9"},
		strAttr("gen_ai.input.messages", "private transcript"),
	)
	report := Analyze(trace(span))
	assertWarning(t, report, "invalid_token_count")
	assertWarning(t, report, "nonconformant_false_compaction")
	assert.Equal(t, "unknown", report.Observations[0].AnswerDrift.State)

	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private transcript")
	assert.Contains(t, report.ClaimCeiling, "no semantic-quality claim")
}

func TestAnalyze_RedactionAndRenderingStability(t *testing.T) {
	secret := "sk-" + strings.Repeat("A", 24)
	in, out := int64(10), int64(2)
	span := responseSpan("a", 1, "resp_a", "", &in, &out, nil)
	span.Attributes = append(span.Attributes,
		strAttr("gen_ai.input.messages", secret),
		strAttr(attrAnswerLabel, "stable"),
	)
	redacted, err := store.Redact(trace(span))
	require.NoError(t, err)
	report := Analyze(redacted)

	var firstJSON, secondJSON, firstText, secondText bytes.Buffer
	require.NoError(t, WriteJSON(&firstJSON, report))
	redactedAgain, err := store.Redact(trace(span))
	require.NoError(t, err)
	require.NoError(t, WriteJSON(&secondJSON, Analyze(redactedAgain)))
	require.NoError(t, WriteText(&firstText, report))
	require.NoError(t, WriteText(&secondText, report))
	assert.Equal(t, firstJSON.String(), secondJSON.String())
	assert.Equal(t, firstText.String(), secondText.String())
	assert.NotContains(t, firstJSON.String(), secret)
	assert.NotContains(t, firstText.String(), secret)
}

func assertWarning(t *testing.T, report Report, code string) {
	t.Helper()
	for _, warning := range report.Warnings {
		if warning.Code == code {
			return
		}
	}
	t.Fatalf("warning %q not found in %#v", code, report.Warnings)
}
