package ledger

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

func TestBuildAgentFixture_ReconcilesExactSources(t *testing.T) {
	report := Build(loadTraceFixture(t, "agent_trace.json"))
	require.NoError(t, ValidateReport(report))
	require.Empty(t, report.Issues, "the clean fixture rollup must reconcile exactly")

	assertCount(t, report.Summary.Usage.Input, 330)
	assertCount(t, report.Summary.Usage.Output, 92)
	assertCount(t, report.Summary.Usage.Total, 422)
	assertCount(t, report.Summary.Usage.CacheRead, 100)
	assertCount(t, report.Summary.Usage.CacheWrite, 25)
	assertCount(t, report.Summary.Usage.Reasoning, 10)
	assertCount(t, report.Summary.Usage.ToolInput, 20)
	assert.Equal(t, 1, report.Summary.ToolCalls)
	assert.Equal(t, 2, report.Summary.RetryAttempts, "retries remain separate consumed attempts")
	require.Equal(t, "known", report.Summary.Context.Status)
	assertCount(t, report.Summary.Context.Limit, 1000)
	assertCount(t, report.Summary.Context.Used, 422)
	assert.InDelta(t, 0.422, *report.Summary.Context.Ratio, 0.000001)

	// The fixture is intentionally out of ingest order. Rows sort by OTel time
	// and stable span ID, and parallel branches retain their top-level owner.
	require.Len(t, report.Rows, 11)
	assert.Equal(t, "0000000000000001", report.Rows[0].SpanID)
	openAI := findRow(t, report, "00000000000000a2")
	assert.Equal(t, "00000000000000a1", openAI.BranchSpanID)
	assertCount(t, openAI.Contribution.CacheRead, 0, "present zero is known")
	assertCount(t, openAI.Contribution.Reasoning, 10)

	anthropic := findRow(t, report, "00000000000000b2")
	assert.Equal(t, "00000000000000b1", anthropic.BranchSpanID)
	assertCount(t, anthropic.Observed.Input, 175)
	assertSource(t, anthropic.Observed.Input, attrAnthropicInput)
	assertSource(t, anthropic.Observed.Input, attrAnthropicCacheRead)
	assertSource(t, anthropic.Observed.Input, attrAnthropicCacheWrite)

	secondSample := findRow(t, report, "00000000000000c2")
	assertCount(t, secondSample.Observed.Input, 25)
	assertCount(t, secondSample.Contribution.Input, 5)
	assertCount(t, secondSample.Contribution.Output, 2)

	rollup := findRow(t, report, "0000000000000001")
	assert.False(t, rollup.Contributes, "rollup is reconciled but not added twice")
	assertCount(t, rollup.Observed.Total, 422)
}

func TestBuildMalformedFixture_FailsUnknownWithDiagnostics(t *testing.T) {
	report := Build(loadTraceFixture(t, "malformed_trace.json"))
	require.NoError(t, ValidateReport(report))
	codes := StableIssueCodes(report)
	assert.Contains(t, codes, "conflicting_usage")
	assert.Contains(t, codes, "negative_count")
	assert.Contains(t, codes, "count_overflow")
	assert.Equal(t, "unknown", report.Summary.Context.Status, "missing context limit must stay UNKNOWN")
	assert.Equal(t, "unknown", report.Summary.Usage.Total.Status)

	conflict := findRow(t, report, "bad-conflict")
	assert.Equal(t, "unknown", conflict.Observed.Input.Status)
	assert.Nil(t, conflict.Observed.Input.Value)
	require.Len(t, conflict.Observed.Input.Sources, 2, "both disagreeing raw fields remain visible")
}

func TestCumulativeDecreaseAndMissingSeries(t *testing.T) {
	tr := model.Trace{TraceID: "cumulative", Spans: []model.Span{
		usageSpan("root", "", 0, map[string]string{}),
		usageSpan("first", "root", 10, map[string]string{attrUsageMode: "cumulative", attrUsageSeries: "s", attrInput: "10", attrOutput: "1"}),
		usageSpan("second", "root", 20, map[string]string{attrUsageMode: "cumulative", attrUsageSeries: "s", attrInput: "9", attrOutput: "2"}),
		usageSpan("missing-series", "root", 30, map[string]string{attrUsageMode: "cumulative", attrInput: "3", attrOutput: "1"}),
	}}
	report := Build(tr)
	codes := StableIssueCodes(report)
	assert.Contains(t, codes, "cumulative_decrease")
	assert.Contains(t, codes, "missing_cumulative_series")
	assert.False(t, findRow(t, report, "missing-series").Contributes)
	assert.Equal(t, "unknown", findRow(t, report, "second").Contribution.Input.Status)
}

func TestRollupDeltaIsExposed(t *testing.T) {
	root := usageSpan("root", "", 0, completeUsageAttrs("10", "2"))
	root.Attributes = append(root.Attributes, model.Attribute{Key: attrUsageMode, ValueType: "str", Value: "rollup"})
	child := usageSpan("child", "root", 1, completeUsageAttrs("9", "2"))
	report := Build(model.Trace{TraceID: "rollup", Spans: []model.Span{root, child}})
	assert.Contains(t, StableIssueCodes(report), "unexplained_delta")
	assertCount(t, report.Summary.Usage.Input, 9)
}

func TestEqualDuplicateCountsOnce(t *testing.T) {
	attrs := completeUsageAttrs("12", "3")
	attrs[attrProvider] = "openai"
	attrs[attrOpenAIInput] = "12"
	report := Build(model.Trace{TraceID: "duplicate", Spans: []model.Span{usageSpan("s", "", 0, attrs)}})
	assert.Contains(t, StableIssueCodes(report), "duplicate_usage")
	assertCount(t, report.Summary.Usage.Input, 12)
}

func TestConflictingContextLimitsAndImpossibleCachePartition(t *testing.T) {
	firstAttrs := completeUsageAttrs("10", "1")
	firstAttrs[attrCacheRead] = "6"
	firstAttrs[attrCacheWrite] = "6"
	firstAttrs[attrContextLimit] = "100"
	secondAttrs := completeUsageAttrs("2", "1")
	secondAttrs[attrContextLimit] = "200"
	report := Build(model.Trace{TraceID: "limits", Spans: []model.Span{
		usageSpan("first", "", 0, firstAttrs),
		usageSpan("second", "", 1, secondAttrs),
	}})
	codes := StableIssueCodes(report)
	assert.Contains(t, codes, "cache_subsets_exceed_input")
	assert.Contains(t, codes, "conflicting_usage")
	assert.Equal(t, "unknown", report.Summary.Context.Status)
	assert.Equal(t, "unknown", findRow(t, report, "first").Observed.CacheRead.Status)
}

func TestRedactionInteractionPreservesNumericLedger(t *testing.T) {
	secret := "sk-" + strings.Repeat("Q", 24)
	sp := usageSpan("s", "", 0, completeUsageAttrs("7", "2"))
	sp.Name = "call " + secret
	redacted, err := store.Redact(model.Trace{TraceID: "redacted", RunLabel: secret, Spans: []model.Span{sp}})
	require.NoError(t, err)
	report := Build(redacted)
	assert.NotContains(t, report.Rows[0].Name, secret)
	assertCount(t, report.Summary.Usage.Total, 9)
}

func TestUserSuppliedRatesAreExactAndProvenanced(t *testing.T) {
	report := Build(loadTraceFixture(t, "agent_trace.json"))
	book, err := LoadRates(filepath.Join("testdata", "rates.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"anthropic/claude-fixture", "openai/gpt-fixture"}, book.SortedRateKeys())
	ApplyRates(&report, book)
	require.NoError(t, ValidateReport(report))
	require.NotNil(t, report.Summary.Estimate)
	require.Equal(t, "known", report.Summary.Estimate.Status)
	assert.Equal(t, "0.00105175", *report.Summary.Estimate.Amount)
	assert.Equal(t, RateSchema, report.Summary.Estimate.Provenance.Schema)
	assert.Len(t, report.Summary.Estimate.Provenance.SHA256, 64)
}

func TestRateFileMalformedAndMissingMatchStayClosed(t *testing.T) {
	_, err := LoadRates(filepath.Join("testdata", "rates_invalid.json"))
	require.Error(t, err)

	fractionPath := filepath.Join(t.TempDir(), "fraction.json")
	require.NoError(t, os.WriteFile(fractionPath, []byte(`{
  "schema":"grotto.token_rates.v1","as_of":"2026-08-11","currency":"USD",
  "rates":[{"provider":"openai","model":"gpt-fixture","per_million_tokens":{"input":"1/2","output":"4"}}]
}`), 0o600))
	_, err = LoadRates(fractionPath)
	require.ErrorContains(t, err, "non-negative decimal string")

	report := Build(loadTraceFixture(t, "agent_trace.json"))
	book := &RateBook{file: RateFile{Rates: []RateEntry{{Provider: "none", Model: "none"}}}, provenance: RateProvenance{Schema: RateSchema, SHA256: strings.Repeat("0", 64)}}
	ApplyRates(&report, book)
	require.NotNil(t, report.Summary.Estimate)
	assert.Equal(t, "unknown", report.Summary.Estimate.Status)
	assert.Nil(t, report.Summary.Estimate.Amount)
}

func TestRenderingAndReportBytesAreDeterministic(t *testing.T) {
	tr := loadTraceFixture(t, "agent_trace.json")
	first, second := Build(tr), Build(tr)
	var firstJSON, secondJSON, firstText, secondText bytes.Buffer
	require.NoError(t, WriteJSON(&firstJSON, first))
	require.NoError(t, WriteJSON(&secondJSON, second))
	require.Equal(t, firstJSON.Bytes(), secondJSON.Bytes())
	require.NoError(t, WriteText(&firstText, first))
	require.NoError(t, WriteText(&secondText, second))
	require.Equal(t, firstText.Bytes(), secondText.Bytes())
	assert.Contains(t, firstText.String(), "cache/context ledger v1")
	assert.Contains(t, firstText.String(), "<- anthropic.usage.input_tokens=50")
}

func TestPublishedSchemasAreValidJSONAndMatchRuntimeIDs(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "schemas", "cache-context-ledger-v1.schema.json"),
		filepath.Join("..", "..", "schemas", "token-rates-v1.schema.json"),
	} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var schema map[string]any
		require.NoError(t, json.Unmarshal(data, &schema))
		assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])
	}

	reportSchema := readSchema(t, filepath.Join("..", "..", "schemas", "cache-context-ledger-v1.schema.json"))
	properties, ok := reportSchema["properties"].(map[string]any)
	require.True(t, ok)
	schemaProperty, ok := properties["schema"].(map[string]any)
	require.True(t, ok)
	schemaConst := schemaProperty["const"]
	assert.Equal(t, ReportSchema, schemaConst)
	rateSchema := readSchema(t, filepath.Join("..", "..", "schemas", "token-rates-v1.schema.json"))
	rateProperties, ok := rateSchema["properties"].(map[string]any)
	require.True(t, ok)
	rateSchemaProperty, ok := rateProperties["schema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, RateSchema, rateSchemaProperty["const"])
}

func TestValidateReportRejectsSchemaAndInvariantViolations(t *testing.T) {
	report := Build(loadTraceFixture(t, "agent_trace.json"))

	wrongSchema := report
	wrongSchema.Schema = "grotto.cache_context_ledger.v0"
	require.ErrorContains(t, ValidateReport(wrongSchema), "schema")

	brokenTotal := report
	brokenTotal.Summary.Usage.Total = knownCount(1, nil)
	require.ErrorContains(t, ValidateReport(brokenTotal), "total does not equal")

	brokenContext := report
	brokenContext.Summary.Context.Status = "unknown"
	require.ErrorContains(t, ValidateReport(brokenContext), "unknown context pressure requires null ratio")
}

func TestLedgerRuntimeHasNoNetworkImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(files, entry.Name(), nil, parser.ImportsOnly)
		require.NoError(t, parseErr)
		for _, spec := range parsed.Imports {
			path, unquoteErr := strconv.Unquote(spec.Path.Value)
			require.NoError(t, unquoteErr)
			assert.Falsef(t, path == "net" || strings.HasPrefix(path, "net/"), "%s imports network package %s", entry.Name(), path)
		}
	}
}

func loadTraceFixture(t *testing.T, name string) model.Trace {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	var tr model.Trace
	require.NoError(t, json.Unmarshal(data, &tr))
	return tr
}

func findRow(t *testing.T, report Report, spanID string) Row {
	t.Helper()
	for _, row := range report.Rows {
		if row.SpanID == spanID {
			return row
		}
	}
	t.Fatalf("row %q not found", spanID)
	return Row{}
}

func assertCount(t *testing.T, count Count, want int64, msgAndArgs ...any) {
	t.Helper()
	require.Equal(t, "known", count.Status, msgAndArgs...)
	require.NotNil(t, count.Value, msgAndArgs...)
	assert.Equal(t, want, *count.Value, msgAndArgs...)
}

func assertSource(t *testing.T, count Count, key string) {
	t.Helper()
	for _, source := range count.Sources {
		if source.Attribute == key {
			return
		}
	}
	t.Errorf("source %q not found in %#v", key, count.Sources)
}

func usageSpan(id, parent string, started int64, values map[string]string) model.Span {
	attrs := make([]model.Attribute, 0, len(values))
	for key, value := range values {
		valueType := "int"
		if key == attrProvider || key == attrUsageMode || key == attrUsageSeries {
			valueType = "str"
		}
		attrs = append(attrs, model.Attribute{Key: key, ValueType: valueType, Value: value})
	}
	return model.Span{SpanID: id, ParentSpanID: parent, Name: id, StartedNs: started, EndedNs: started + 1, DurationNs: 1, Attributes: attrs}
}

func completeUsageAttrs(input, output string) map[string]string {
	return map[string]string{
		attrInput: input, attrOutput: output, attrCacheRead: "0", attrCacheWrite: "0",
		attrReasoning: "0", attrToolInput: "0",
	}
}

func readSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(data, &schema))
	return schema
}
