package render

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
)

func criticalPathFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "tests", "fixtures", "critical-path")
}

func loadCriticalPathTrace(t *testing.T, name string) model.Trace {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(criticalPathFixtureDir(t), name))
	require.NoError(t, err)
	var tr model.Trace
	require.NoError(t, json.Unmarshal(data, &tr))
	return tr
}

func TestAnalyzeCriticalPath_GoldenFixtures(t *testing.T) {
	cases := []struct {
		trace  string
		golden string
		status string
	}{
		{"diamond-trace.json", "diamond.v1.json", criticalPathStatusOK},
		{"no-edge-trace.json", "no-edge.v1.json", criticalPathStatusNoEdge},
		{"cyclic-trace.json", "cyclic.v1.json", criticalPathStatusCyclic},
		{"missing-trace.json", "missing.v1.json", criticalPathStatusMissing},
		{"malformed-trace.json", "malformed.v1.json", criticalPathStatusMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			tr := loadCriticalPathTrace(t, tc.trace)
			report := AnalyzeCriticalPath(tr)
			require.Equal(t, tc.status, report.Status)
			require.NoError(t, ValidateCriticalPathReport(report))

			var got bytes.Buffer
			require.NoError(t, WriteCriticalPathJSON(&got, report))

			golden := filepath.Join(criticalPathFixtureDir(t), tc.golden)
			if os.Getenv("GROTTO_UPDATE_GOLDEN") != "" {
				require.NoError(t, os.WriteFile(golden, got.Bytes(), 0o644))
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err)
			assert.Equal(t, string(want), got.String())

			var decoded CriticalPathReport
			require.NoError(t, json.Unmarshal(got.Bytes(), &decoded))
			assert.Equal(t, CriticalPathSchema, decoded.Schema)
			assert.Equal(t, tc.status, decoded.Status)
			if tc.status != criticalPathStatusOK {
				assert.Empty(t, decoded.Spans, "non-ok graphs must not invent a path")
				assert.Nil(t, decoded.Metrics.CriticalNs)
			}
		})
	}
}

func TestAnalyzeCriticalPath_DiamondMatchesTextPath(t *testing.T) {
	tr := loadCriticalPathTrace(t, "diamond-trace.json")
	report := AnalyzeCriticalPath(tr)
	require.Equal(t, criticalPathStatusOK, report.Status)
	require.Len(t, report.Spans, 3)
	assert.Equal(t, []string{"A", "B", "D"}, []string{report.Spans[0].Name, report.Spans[1].Name, report.Spans[2].Name})
	require.NotNil(t, report.Metrics.CriticalNs)
	assert.Equal(t, int64(500), *report.Metrics.CriticalNs)
	assert.Equal(t, int64(550), report.Metrics.TotalWorkNs)
	assert.Equal(t, 4, report.Metrics.UnitCount)
	assert.Equal(t, 4, report.Metrics.EdgeCount)

	cp, ok := ComputeCriticalPath(tr)
	require.True(t, ok)
	assert.Equal(t, []string{"A", "B", "D"}, []string{cp.Spans[0].Name, cp.Spans[1].Name, cp.Spans[2].Name})
	assert.Equal(t, cp.CriticalNs, *report.Metrics.CriticalNs)
}

func TestAnalyzeCriticalPath_NoEdgeDoesNotInventPath(t *testing.T) {
	tr := loadCriticalPathTrace(t, "no-edge-trace.json")
	report := AnalyzeCriticalPath(tr)
	assert.Equal(t, criticalPathStatusNoEdge, report.Status)
	assert.Empty(t, report.Spans)
	assert.Nil(t, report.Metrics.CriticalNs)
	assert.Equal(t, "no_edge", report.Issues[0].Code)

	_, ok := ComputeCriticalPath(tr)
	assert.False(t, ok, "text helper still reports no edges")
}

func TestAnalyzeCriticalPath_CycleDoesNotInventPath(t *testing.T) {
	tr := loadCriticalPathTrace(t, "cyclic-trace.json")
	report := AnalyzeCriticalPath(tr)
	assert.Equal(t, criticalPathStatusCyclic, report.Status)
	assert.Empty(t, report.Spans)
	assert.Nil(t, report.Metrics.CriticalNs)
	assert.True(t, hasCriticalPathIssue(report, issueCycle))

	cp, ok := ComputeCriticalPath(tr)
	assert.True(t, ok, "text helper still terminates with a degraded path")
	assert.NotEmpty(t, cp.Spans)
}

func TestAnalyzeCriticalPath_SelfEdgeIsCyclic(t *testing.T) {
	tr := model.Trace{
		TraceID: "self-edge", DurationNs: 50,
		Spans: []model.Span{unitSpan(0, "loop", 0, 50, 0)},
	}
	report := AnalyzeCriticalPath(tr)
	assert.Equal(t, criticalPathStatusCyclic, report.Status)
	assert.Empty(t, report.Spans)
	assert.True(t, hasCriticalPathIssue(report, issueCycle))
}

func TestAnalyzeCriticalPath_MissingUnitDoesNotInventPath(t *testing.T) {
	tr := loadCriticalPathTrace(t, "missing-trace.json")
	report := AnalyzeCriticalPath(tr)
	assert.Equal(t, criticalPathStatusMissing, report.Status)
	assert.Empty(t, report.Spans)
	assert.Nil(t, report.Metrics.CriticalNs)
	assert.Equal(t, 2, report.Metrics.UnitCount)
	assert.Equal(t, 1, report.Metrics.EdgeCount)
	require.True(t, hasCriticalPathIssue(report, issueMissingUnit))
	assert.Equal(t, 99, *findCriticalPathIssue(t, report, issueMissingUnit).Unit)
}

func TestAnalyzeCriticalPath_MalformedDoesNotInventPath(t *testing.T) {
	tr := loadCriticalPathTrace(t, "malformed-trace.json")
	report := AnalyzeCriticalPath(tr)
	assert.Equal(t, criticalPathStatusMalformed, report.Status)
	assert.Empty(t, report.Spans)
	assert.True(t, hasCriticalPathIssue(report, issueDuplicateUnit))
	assert.True(t, hasCriticalPathIssue(report, issueInvalidUnit))
	assert.True(t, hasCriticalPathIssue(report, issueInvalidUnblocks))
	assert.True(t, hasCriticalPathIssue(report, issueUnblocksWithoutUnit))
}

func TestAnalyzeCriticalPath_EqualBranchesAreDeterministic(t *testing.T) {
	tr := model.Trace{
		TraceID: "tie", DurationNs: 200,
		Spans: []model.Span{
			unitSpan(0, "A", 0, 100, 1, 2),
			unitSpan(1, "B", 100, 50, 3),
			unitSpan(2, "C", 100, 50, 3),
			unitSpan(3, "D", 150, 50),
		},
	}
	first := AnalyzeCriticalPath(tr)
	second := AnalyzeCriticalPath(tr)
	require.Equal(t, criticalPathStatusOK, first.Status)
	assert.Equal(t, []string{"A", "B", "D"}, []string{first.Spans[0].Name, first.Spans[1].Name, first.Spans[2].Name})
	var firstJSON, secondJSON bytes.Buffer
	require.NoError(t, WriteCriticalPathJSON(&firstJSON, first))
	require.NoError(t, WriteCriticalPathJSON(&secondJSON, second))
	assert.Equal(t, firstJSON.Bytes(), secondJSON.Bytes())
}

func TestAnalyzeCriticalPath_IsolatedUnitsPickLongest(t *testing.T) {
	tr := model.Trace{
		TraceID: "isolated", DurationNs: 300,
		Spans: []model.Span{
			unitSpan(2, "short", 0, 10),
			unitSpan(5, "long", 0, 300),
			unitSpan(1, "mid", 0, 50),
		},
	}
	report := AnalyzeCriticalPath(tr)
	require.NoError(t, ValidateCriticalPathReport(report))
	require.Equal(t, criticalPathStatusOK, report.Status)
	require.Len(t, report.Spans, 1)
	assert.Equal(t, "long", report.Spans[0].Name)
	assert.Equal(t, 5, report.Spans[0].Unit)
	assert.Equal(t, 0, report.Metrics.EdgeCount)
}

func TestWriteCriticalPathJSON_DeterministicBytes(t *testing.T) {
	tr := loadCriticalPathTrace(t, "diamond-trace.json")
	var first, second bytes.Buffer
	require.NoError(t, WriteCriticalPathJSON(&first, AnalyzeCriticalPath(tr)))
	require.NoError(t, WriteCriticalPathJSON(&second, AnalyzeCriticalPath(tr)))
	assert.Equal(t, first.Bytes(), second.Bytes())
}

func TestPublishedCriticalPathSchemaMatchesRuntimeIDs(t *testing.T) {
	path := filepath.Join("..", "..", "schemas", "critical-path-v1.schema.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(data, &schema))
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])

	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	schemaProperty, ok := properties["schema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, CriticalPathSchema, schemaProperty["const"])
	claim, ok := properties["claim_ceiling"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, CriticalPathClaimCeiling, claim["const"])

	status, ok := properties["status"].(map[string]any)
	require.True(t, ok)
	enum, ok := status["enum"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"ok", "no_edge", "missing", "cyclic", "malformed"}, enum)
}

func TestValidateCriticalPathReportRejectsSchemaAndPathInventions(t *testing.T) {
	report := AnalyzeCriticalPath(loadCriticalPathTrace(t, "diamond-trace.json"))

	wrongSchema := report
	wrongSchema.Schema = "grotto.critical_path.v0"
	require.ErrorContains(t, ValidateCriticalPathReport(wrongSchema), "schema")

	invented := AnalyzeCriticalPath(loadCriticalPathTrace(t, "no-edge-trace.json"))
	invented.Spans = []CriticalPathSpan{{Index: 0, Unit: 0, SpanID: "invented", DurationNs: 1}}
	invented.Metrics.PathSpanCount = 1
	require.ErrorContains(t, ValidateCriticalPathReport(invented), "must not invent a path")

	okMissingNs := report
	okMissingNs.Metrics.CriticalNs = nil
	require.ErrorContains(t, ValidateCriticalPathReport(okMissingNs), "critical_ns")
}

func TestCriticalPathJSONRuntimeHasNoNetworkImports(t *testing.T) {
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

func hasCriticalPathIssue(report CriticalPathReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func findCriticalPathIssue(t *testing.T, report CriticalPathReport, code string) CriticalPathIssue {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Code == code {
			return issue
		}
	}
	t.Fatalf("issue %q not found in %#v", code, report.Issues)
	return CriticalPathIssue{}
}
