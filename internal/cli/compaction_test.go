package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compactionFixture(name string) string {
	return filepath.Join("..", "..", "tests", "fixtures", "compaction", name)
}

func TestLoadOTLPJSON_OneTraceWithRealLink(t *testing.T) {
	trace, err := loadOTLPJSON(compactionFixture("one_compaction.otlp.json"))
	require.NoError(t, err)
	require.Len(t, trace.Spans, 3)
	assert.Equal(t, "11111111111111111111111111111111", trace.TraceID)

	var linked bool
	for _, span := range trace.Spans {
		if span.SpanID == "0000000000000003" {
			require.Len(t, span.Links, 1)
			assert.Equal(t, "0000000000000002", span.Links[0].SpanID)
			linked = true
		}
	}
	assert.True(t, linked, "compacted response span not found")
}

func TestCompactionCommand_FixtureMatchesGoldenAndJSONContract(t *testing.T) {
	fixture := compactionFixture("one_compaction.otlp.json")
	want, err := os.ReadFile(compactionFixture("expected_one_compaction.txt"))
	require.NoError(t, err)

	root := NewRootCmd()
	var text bytes.Buffer
	root.SetOut(&text)
	root.SetArgs([]string{"compaction", "--otlp-json", fixture})
	require.NoError(t, root.Execute())
	assert.Equal(t, string(want), text.String())

	root = NewRootCmd()
	var machine bytes.Buffer
	root.SetOut(&machine)
	root.SetArgs([]string{"compaction", "--otlp-json", fixture, "--json"})
	require.NoError(t, root.Execute())
	assert.Contains(t, machine.String(), `"schema": "grotto.compaction_report.v1"`)
	assert.Contains(t, machine.String(), `"status": "linked"`)
	assert.NotContains(t, machine.String(), "input.messages")
}

func TestLoadOTLPJSON_MalformedAndNoNetworkBehavior(t *testing.T) {
	_, err := loadOTLPJSON(compactionFixture("malformed.otlp.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode OTLP JSON")

	_, err = loadOTLPJSON("https://example.com/private-trace.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open OTLP JSON", "URLs are treated as local paths; no network fetch exists")
}

func TestLoadOTLPJSON_RejectsMultipleTraces(t *testing.T) {
	data, err := os.ReadFile(compactionFixture("one_compaction.otlp.json"))
	require.NoError(t, err)
	// Replacing one response trace ID yields a second trace without introducing
	// any network or model dependency.
	data = bytes.Replace(data, []byte("EREREREREREREREREREREQ=="), []byte("IiIiIiIiIiIiIiIiIiIiIg=="), 1)
	path := filepath.Join(t.TempDir(), "two-traces.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	_, err = loadOTLPJSON(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected exactly one")
}
