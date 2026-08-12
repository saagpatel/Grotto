package redaction

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

func TestDefaultEvaluator_CoversSensitiveTraceFieldsWithoutRawReportContent(t *testing.T) {
	secret := "ghp_" + strings.Repeat("x", 36)
	original := model.Trace{
		TraceID: "trace-preview", RunLabel: "deploy " + secret, RootName: "root", Source: "otlp",
		Spans: []model.Span{{
			SpanID: "span-1", TraceID: "trace-preview", Name: "request",
			Attributes: []model.Attribute{
				{Key: "authorization", ValueType: "str", Value: "Bearer fake-never-real-token"},
				{Key: "http.request.header.cookie", ValueType: "str", Value: "session=fake-cookie"},
				{Key: "user.email", ValueType: "str", Value: "operator@example.test"},
				{Key: "workspace", ValueType: "str", Value: "/Users/example/project"},
				{Key: "url.full", ValueType: "str", Value: "https://example.test/path?token=fake"},
				{Key: "gen_ai.input.messages", ValueType: "json", Value: `[{"role":"user","content":"fake prompt"}]`},
				{Key: "gen_ai.output.messages", ValueType: "json", Value: `[{"role":"assistant","content":"fake completion"}]`},
				{Key: "gen_ai.tool.call.arguments", ValueType: "json", Value: `{"city":"Example"}`},
				{Key: "gen_ai.tool.call.result", ValueType: "json", Value: `{"result":"fake"}`},
				{Key: "payload", ValueType: "json", Value: `{"nested":{"authorization":"Bearer fake-nested-token","safe":"ok"}}`},
				{Key: "blob", ValueType: "bytes", Value: "00ff1020"},
				{Key: "custom.safe", ValueType: "str", Value: "ordinary metadata"},
			},
		}},
	}
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	result, err := evaluator.Evaluate(original, Options{SourceKind: "file", SourceRef: "synthetic.json"})
	require.NoError(t, err)

	require.Equal(t, secret, strings.TrimPrefix(original.RunLabel, "deploy "), "input trace must remain untouched")
	reportJSON, err := json.Marshal(result.Report)
	require.NoError(t, err)
	for _, raw := range []string{secret, "fake-never-real-token", "fake-cookie", "operator@example.test", "/Users/example", "fake prompt", "fake completion", "fake-nested-token"} {
		assert.NotContains(t, string(reportJSON), raw)
	}
	assert.Greater(t, result.Report.Summary.Masked, 0)
	assert.Greater(t, result.Report.Summary.Hashed, 0)
	assert.Greater(t, result.Report.Summary.Dropped, 0)
	assert.Greater(t, result.Report.Summary.Retained, 0)

	attrs := attrsByKey(result.Trace.Spans[0].Attributes)
	assert.NotContains(t, attrs, "gen_ai.input.messages")
	assert.NotContains(t, attrs, "gen_ai.output.messages")
	assert.NotContains(t, attrs, "gen_ai.tool.call.arguments")
	assert.NotContains(t, attrs, "gen_ai.tool.call.result")
	assert.Contains(t, attrs["authorization"].Value, "‹redacted›")
	assert.NotContains(t, attrs["authorization"].Value, "fake-never-real-token")
	assert.True(t, strings.HasPrefix(attrs["blob"].Value, "sha256:v1:"))
	assert.Equal(t, "ordinary metadata", attrs["custom.safe"].Value)
	assert.NotContains(t, attrs["payload"].Value, "fake-nested-token")
}

func TestRulePrecedence_AllowlistAndLexicalTieBreakAreDeterministic(t *testing.T) {
	policy := testPolicy([]Rule{
		{ID: "block.email", Priority: 100, Path: "*", ValueRegex: `(?i).+@example\.test`, Category: "email", Action: ActionMask, Explanation: "block", Provenance: "test"},
		{ID: "allow.support", Priority: 200, Path: `spans[*].attributes["contact"]`, ValueRegex: `support@example\.test`, Category: "allowlist", Action: ActionRetain, Explanation: "allow exact fixture", Provenance: "test"},
		{ID: "z.tie", Priority: 50, Path: `spans[*].attributes["tie"]`, Category: "tie", Action: ActionDrop, Explanation: "z", Provenance: "test"},
		{ID: "a.tie", Priority: 50, Path: `spans[*].attributes["tie"]`, Category: "tie", Action: ActionMask, Explanation: "a", Provenance: "test"},
	})
	evaluator, err := NewEvaluator(policy)
	require.NoError(t, err)
	trace := traceWithAttrs(
		model.Attribute{Key: "contact", ValueType: "str", Value: "support@example.test"},
		model.Attribute{Key: "tie", ValueType: "str", Value: "fixture"},
	)
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "precedence"})
	require.NoError(t, err)
	decisions := decisionsByPath(result.Report.Decisions)
	assert.Equal(t, "allow.support", decisions[`spans[0].attributes["contact"]`].MatchedRule)
	assert.Equal(t, ActionRetain, decisions[`spans[0].attributes["contact"]`].Action)
	assert.Equal(t, "a.tie", decisions[`spans[0].attributes["tie"]`].MatchedRule)
}

func TestEvaluator_IdempotentWithStableBinaryDigest(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	trace := traceWithAttrs(model.Attribute{Key: "blob", ValueType: "bytes", Value: "00ff1020"})
	first, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "one"})
	require.NoError(t, err)
	second, err := evaluator.Evaluate(first.Trace, Options{SourceKind: "test", SourceRef: "two"})
	require.NoError(t, err)
	assert.Equal(t, first.Trace, second.Trace)
	assert.Equal(t, stableDigest("00ff1020"), first.Trace.Spans[0].Attributes[0].Value)
}

func TestEvaluator_IdempotentAcrossMaskDropHashAndNestedJSON(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	trace := traceWithAttrs(
		model.Attribute{Key: "authorization", ValueType: "str", Value: "Bearer fake-idempotent-token"},
		model.Attribute{Key: "gen_ai.input.messages", ValueType: "json", Value: `[{"content":"fixture"}]`},
		model.Attribute{Key: "blob", ValueType: "bytes", Value: "00ff"},
		model.Attribute{Key: "payload", ValueType: "json", Value: `{"authorization":"Bearer fake-nested-token","safe":"ok"}`},
	)
	first, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "first"})
	require.NoError(t, err)
	second, err := evaluator.Evaluate(first.Trace, Options{SourceKind: "test", SourceRef: "second"})
	require.NoError(t, err)
	assert.Equal(t, first.Trace, second.Trace)
}

func TestEvaluator_MasksEveryCandidateKindInOneField(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	secret := "ghp_" + strings.Repeat("q", 36)
	value := "token=" + secret + " email=person@example.test path=/Users/example/project"
	trace := traceWithAttrs(model.Attribute{Key: "note", ValueType: "str", Value: value})
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "multi"})
	require.NoError(t, err)
	applied := result.Trace.Spans[0].Attributes[0].Value
	assert.NotContains(t, applied, secret)
	assert.NotContains(t, applied, "person@example.test")
	assert.NotContains(t, applied, "/Users/example")
	var out bytes.Buffer
	require.NoError(t, WriteJSON(&out, result.Report))
	assert.NotContains(t, out.String(), secret)
	assert.NotContains(t, out.String(), "person@example.test")
	assert.NotContains(t, out.String(), "/Users/example")
}

func TestEvaluator_MaskReplacementCannotExpandCapturedRawValue(t *testing.T) {
	policy := testPolicy([]Rule{{
		ID: "mask", Priority: 10, Path: "*", ValueRegex: `(candidate-secret)`,
		Category: "secret", Action: ActionMask, Explanation: "mask", Provenance: "test", Replacement: "$1-$0",
	}})
	evaluator, err := NewEvaluator(policy)
	require.NoError(t, err)
	result, err := evaluator.Evaluate(
		traceWithAttrs(model.Attribute{Key: "note", ValueType: "str", Value: "candidate-secret"}),
		Options{SourceKind: "test", SourceRef: "replacement"},
	)
	require.NoError(t, err)
	assert.Equal(t, "$1-$0", result.Trace.Spans[0].Attributes[0].Value)
	var out bytes.Buffer
	require.NoError(t, WriteJSON(&out, result.Report))
	assert.NotContains(t, out.String(), "candidate-secret")
}

func TestEvaluator_UnicodeTruncationIsValidAndBounded(t *testing.T) {
	policy := testPolicy(nil)
	policy.MaxValueBytes = 32
	evaluator, err := NewEvaluator(policy)
	require.NoError(t, err)
	trace := traceWithAttrs(model.Attribute{Key: "unicode", ValueType: "str", Value: strings.Repeat("界", 30)})
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "unicode"})
	require.NoError(t, err)
	value := result.Trace.Spans[0].Attributes[0].Value
	assert.True(t, utf8.ValidString(value))
	assert.LessOrEqual(t, len(value), 32)
	assert.Equal(t, ActionTruncate, decisionsByPath(result.Report.Decisions)[`spans[0].attributes["unicode"]`].Action)
}

func TestEvaluator_MalformedJSONAndDepthLimitAreUnknownFailClosed(t *testing.T) {
	policy := testPolicy(nil)
	policy.MaxDepth = 2
	evaluator, err := NewEvaluator(policy)
	require.NoError(t, err)
	trace := traceWithAttrs(
		model.Attribute{Key: "malformed", ValueType: "json", Value: `{"secret":`},
		model.Attribute{Key: "deep", ValueType: "json", Value: `{"a":{"b":{"c":"value"}}}`},
	)
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "unknown"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Report.Summary.Unknown, 2)
	assert.NotContains(t, attrsByKey(result.Trace.Spans[0].Attributes), "malformed")
	for _, decision := range result.Report.Decisions {
		if decision.Status == "unknown" {
			assert.Equal(t, ActionDrop, decision.Action)
			assert.NotEmpty(t, decision.UnknownReason)
		}
	}
}

func TestEvaluator_UnknownValueTypeIsNotReflectedAndFailsClosed(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	trace := traceWithAttrs(model.Attribute{Key: "custom", ValueType: "secret-type-candidate", Value: "ordinary"})
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "unknown-type"})
	require.NoError(t, err)
	assert.Empty(t, result.Trace.Spans[0].Attributes)
	var out bytes.Buffer
	require.NoError(t, WriteJSON(&out, result.Report))
	assert.NotContains(t, out.String(), "secret-type-candidate")
	assert.Contains(t, out.String(), "unknown_value_type")
}

func TestEvaluator_FalsePositiveControls(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	trace := traceWithAttrs(
		model.Attribute{Key: "note", ValueType: "str", Value: "sk-short"},
		model.Attribute{Key: "build.id", ValueType: "str", Value: "AKIA-not-a-key"},
	)
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "ordinary"})
	require.NoError(t, err)
	attrs := attrsByKey(result.Trace.Spans[0].Attributes)
	assert.Equal(t, "sk-short", attrs["note"].Value)
	assert.Equal(t, "AKIA-not-a-key", attrs["build.id"].Value)
}

func TestDefaultEvaluator_RetainsTelemetryTokenCountsButStillMasksCredentials(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	secret := "sk-" + strings.Repeat("S", 24)
	trace := traceWithAttrs(
		model.Attribute{Key: "gen_ai.usage.input_tokens", ValueType: "int", Value: "1200"},
		model.Attribute{Key: "openai.usage.input_tokens_details.cached_tokens", ValueType: "int", Value: "300"},
		model.Attribute{Key: "grotto.context.window.limit_tokens", ValueType: "int", Value: "128000"},
		model.Attribute{Key: "gen_ai.usage.output_tokens", ValueType: "str", Value: secret},
	)
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "counts"})
	require.NoError(t, err)
	attrs := attrsByKey(result.Trace.Spans[0].Attributes)
	assert.Equal(t, model.Attribute{Key: "gen_ai.usage.input_tokens", ValueType: "int", Value: "1200"}, attrs["gen_ai.usage.input_tokens"])
	assert.Equal(t, model.Attribute{Key: "openai.usage.input_tokens_details.cached_tokens", ValueType: "int", Value: "300"}, attrs["openai.usage.input_tokens_details.cached_tokens"])
	assert.Equal(t, model.Attribute{Key: "grotto.context.window.limit_tokens", ValueType: "int", Value: "128000"}, attrs["grotto.context.window.limit_tokens"])
	assert.Equal(t, "‹redacted›", attrs["gen_ai.usage.output_tokens"].Value)
}

func TestWritersNeverRenderRetainedRawValues(t *testing.T) {
	evaluator, err := DefaultEvaluator()
	require.NoError(t, err)
	trace := traceWithAttrs(model.Attribute{Key: "custom.safe", ValueType: "str", Value: "candidate-raw-value"})
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "writer"})
	require.NoError(t, err)
	var textOut, jsonOut bytes.Buffer
	require.NoError(t, WriteText(&textOut, result.Report))
	require.NoError(t, WriteJSON(&jsonOut, result.Report))
	assert.NotContains(t, textOut.String(), "candidate-raw-value")
	assert.NotContains(t, jsonOut.String(), "candidate-raw-value")
	assert.Contains(t, textOut.String(), retainedPreview)
}

func TestWritersNeverRenderRawTruncatedContent(t *testing.T) {
	policy := testPolicy([]Rule{{
		ID: "truncate", Priority: 10, Path: `spans[*].attributes["exception.message"]`,
		Category: "exception", Action: ActionTruncate, Explanation: "bound", Provenance: "test", MaxLength: 32,
	}})
	evaluator, err := NewEvaluator(policy)
	require.NoError(t, err)
	trace := traceWithAttrs(model.Attribute{Key: "exception.message", ValueType: "str", Value: "candidate exception body must stay out of reports"})
	result, err := evaluator.Evaluate(trace, Options{SourceKind: "test", SourceRef: "truncate"})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, WriteJSON(&out, result.Report))
	assert.NotContains(t, out.String(), "candidate exception body")
	assert.Contains(t, out.String(), "<truncated:")
}

func testPolicy(rules []Rule) Policy {
	return Policy{
		Schema: PolicySchemaV1, PolicyID: "test.policy", Version: "1.0.0",
		Description: "synthetic", MaxDepth: 8, MaxValueBytes: 1024,
		Rules: rules,
	}
}

func traceWithAttrs(attrs ...model.Attribute) model.Trace {
	return model.Trace{
		TraceID: "trace", RunLabel: "run", RootName: "root", Source: "otlp", SpanCount: 1,
		Spans: []model.Span{{SpanID: "span", TraceID: "trace", Name: "root", Attributes: attrs}},
	}
}

func attrsByKey(attrs []model.Attribute) map[string]model.Attribute {
	out := make(map[string]model.Attribute, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr
	}
	return out
}

func decisionsByPath(decisions []Decision) map[string]Decision {
	out := make(map[string]Decision, len(decisions))
	for _, decision := range decisions {
		out[decision.Path] = decision
	}
	return out
}
