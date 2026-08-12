package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/redaction"
	"github.com/saagpatel/grotto/internal/store"
)

func TestRedactPreview_FileIsByteStableAndRawContentOff(t *testing.T) {
	secret := "sk-" + strings.Repeat("F", 32)
	path := filepath.Join(t.TempDir(), "synthetic trace.json")
	trace := previewTrace(secret)
	b, err := json.MarshalIndent(trace, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
	before := digestFile(t, path)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"redact-preview", "--file", path, "--json"})
	require.NoError(t, root.Execute())

	assert.Equal(t, before, digestFile(t, path))
	assert.Contains(t, out.String(), "grotto.redaction-preview.v1")
	assert.NotContains(t, out.String(), secret)
	assert.NotContains(t, out.String(), "candidate prompt")
	assert.NotContains(t, out.String(), "/Users/example")
}

func TestRedactPreview_StoredTraceDoesNotChangeSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stored preview.db")
	t.Setenv("GROTTO_DB", dbPath)
	ctx := t.Context()
	st, err := store.Open(ctx, dbPath)
	require.NoError(t, err)
	require.NoError(t, st.InsertTrace(ctx, previewTrace("ordinary")))
	require.NoError(t, st.Close())
	before := digestFile(t, dbPath)
	beforeSidecars, err := filepath.Glob(dbPath + "-*")
	require.NoError(t, err)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"redact-preview", "trace-preview"})
	require.NoError(t, root.Execute())

	assert.Equal(t, before, digestFile(t, dbPath))
	afterSidecars, err := filepath.Glob(dbPath + "-*")
	require.NoError(t, err)
	assert.Equal(t, beforeSidecars, afterSidecars)
	assert.Contains(t, out.String(), "redaction preview")
}

func TestRedactPreview_RequiresExactlyOneSource(t *testing.T) {
	for _, args := range [][]string{
		{"redact-preview"},
		{"redact-preview", "trace", "--file", "fixture.json"},
	} {
		root := NewRootCmd()
		root.SetArgs(args)
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one source")
	}
}

func TestRedactPreview_EdgeFixturesExerciseEveryActionAndUnknown(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"redact-preview",
		"--file", filepath.Join("..", "..", "tests", "fixtures", "redaction", "edge-cases-trace.json"),
		"--policy", filepath.Join("..", "..", "tests", "fixtures", "redaction", "conflict-policy.json"),
		"--json",
	})
	require.NoError(t, root.Execute())
	var report redaction.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	assert.Greater(t, report.Summary.Retained, 0)
	assert.Greater(t, report.Summary.Masked, 0)
	assert.Greater(t, report.Summary.Hashed, 0)
	assert.Greater(t, report.Summary.Truncated, 0)
	assert.Greater(t, report.Summary.Dropped, 0)
	assert.Greater(t, report.Summary.Unknown, 0)
	assert.NotContains(t, out.String(), "other@example.test")
	assert.NotContains(t, out.String(), "fake-only")
}

func previewTrace(secret string) model.Trace {
	return model.Trace{
		TraceID: "trace-preview", RunLabel: "run " + secret, RootName: "root", Source: "otlp",
		SpanCount: 1,
		Spans: []model.Span{{
			SpanID: "span-preview", TraceID: "trace-preview", Name: "request",
			Attributes: []model.Attribute{
				{Key: "authorization", ValueType: "str", Value: "Bearer fake-preview-token"},
				{Key: "user.email", ValueType: "str", Value: "person@example.test"},
				{Key: "workspace", ValueType: "str", Value: "/Users/example/project"},
				{Key: "gen_ai.input.messages", ValueType: "json", Value: `[{"content":"candidate prompt"}]`},
			},
		}},
	}
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
