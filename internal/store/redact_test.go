package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

var (
	awsKey    = "AKIA" + strings.Repeat("Q", 16)
	githubPAT = "ghp_" + strings.Repeat("a", 36)
	openAIKey = "sk-" + strings.Repeat("B", 32)
	slackTok  = "xoxb-" + strings.Repeat("1", 24)
)

func TestRedact_LegacyCredentialRegression(t *testing.T) {
	for _, secret := range []string{awsKey, githubPAT, openAIKey, slackTok} {
		original := model.Trace{
			RunLabel: "deploy with " + secret,
			Spans: []model.Span{{
				Name:       "auth " + secret,
				Attributes: []model.Attribute{{Key: "note", ValueType: "str", Value: secret}},
			}},
		}
		got, err := Redact(original)
		require.NoError(t, err)
		assert.NotContains(t, got.RunLabel, secret)
		assert.Contains(t, got.RunLabel, "‹redacted›")
		assert.NotContains(t, got.Spans[0].Name, secret)
		assert.Equal(t, "‹redacted›", got.Spans[0].Attributes[0].Value)
		assert.Equal(t, "deploy with "+secret, original.RunLabel, "input must not be mutated")
	}
}

func TestInsertTrace_UsesCanonicalEvaluatorBeforePersisting(t *testing.T) {
	st, ctx := newTestStore(t)
	tr := model.Trace{
		TraceID: "redact-1", RunLabel: "push " + githubPAT, Source: "mark", RootName: "push",
		StartedNs: 0, EndedNs: 100, DurationNs: 100, SpanCount: 1,
		Spans: []model.Span{{
			SpanID: "s1", TraceID: "redact-1", Name: "auth " + githubPAT,
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: 0, EndedNs: 100, DurationNs: 100,
			Attributes: []model.Attribute{
				{Key: "authorization", ValueType: "str", Value: "Bearer fake-never-real-token"},
				{Key: "gen_ai.input.messages", ValueType: "json", Value: `[{"role":"user","content":"fake private prompt"}]`},
			},
		}},
	}
	require.NoError(t, st.InsertTrace(ctx, tr))
	got, err := st.GetTrace(ctx, "redact-1")
	require.NoError(t, err)
	assert.NotContains(t, got.RunLabel, githubPAT)
	require.Len(t, got.Spans[0].Attributes, 1, "GenAI content must be dropped at ingest")
	assert.Equal(t, "authorization", got.Spans[0].Attributes[0].Key)
	assert.NotContains(t, got.Spans[0].Attributes[0].Value, "fake-never-real-token")
}

func TestOpenReadOnly_DoesNotChangeDatabaseOrCreateSidecars(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "preview.db")
	st, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, st.InsertTrace(ctx, minimalTrace("read-only", "fixture", 1)))
	require.NoError(t, st.Close())

	before := fileDigest(t, path)
	beforeSidecars, err := filepath.Glob(path + "-*")
	require.NoError(t, err)
	ro, err := OpenReadOnly(ctx, path)
	require.NoError(t, err)
	_, err = ro.GetTrace(ctx, "read-only")
	require.NoError(t, err)
	require.NoError(t, ro.Close())

	assert.Equal(t, before, fileDigest(t, path))
	afterSidecars, err := filepath.Glob(path + "-*")
	require.NoError(t, err)
	assert.Equal(t, beforeSidecars, afterSidecars)
}

func TestOpenReadOnly_ResolvesRelativeDatabasePath(t *testing.T) {
	t.Chdir(t.TempDir())
	const path = "relative-preview.db"
	ctx := t.Context()
	st, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, st.InsertTrace(ctx, minimalTrace("relative", "fixture", 1)))
	require.NoError(t, st.Close())

	ro, err := OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ro.Close()) })
	got, err := ro.GetTrace(ctx, "relative")
	require.NoError(t, err)
	assert.Equal(t, "relative", got.TraceID)
}

func TestOpenReadOnly_ObservesLaterCommittedTrace(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "live-preview.db")
	writer, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	require.NoError(t, writer.InsertTrace(ctx, minimalTrace("before", "fixture", 1)))

	ro, err := OpenReadOnly(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ro.Close()) })
	_, err = ro.GetTrace(ctx, "before")
	require.NoError(t, err)

	require.NoError(t, writer.InsertTrace(ctx, minimalTrace("after", "fixture", 2)))
	got, err := ro.GetTrace(ctx, "after")
	require.NoError(t, err)
	assert.Equal(t, "after", got.TraceID)
}

func TestOpenReadOnly_LoadsPreSpanLinkSchemaWithoutMigrating(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "pre-span-links.db")
	schema, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_init.sql"))
	require.NoError(t, err)
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(schema))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO traces (trace_id, run_label, source, root_name, started_ns, ended_ns, duration_ns, span_count, created_at)
		VALUES ('legacy', 'fixture', 'mark', 'root', 0, 100, 100, 1, 0);
		INSERT INTO spans (span_id, trace_id, parent_span_id, name, kind, status_code, started_ns, ended_ns, duration_ns)
		VALUES ('legacy-root', 'legacy', NULL, 'root', 1, 1, 0, 100, 100);
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	before := fileDigest(t, path)
	ro, err := OpenReadOnly(ctx, path)
	require.NoError(t, err)
	got, err := ro.GetTrace(ctx, "legacy")
	require.NoError(t, err)
	require.NoError(t, ro.Close())
	require.Len(t, got.Spans, 1)
	assert.Equal(t, "legacy-root", got.Spans[0].SpanID)
	assert.Zero(t, got.Spans[0].DroppedAttributesCount)
	assert.Zero(t, got.Spans[0].DroppedLinksCount)
	assert.Empty(t, got.Spans[0].Links)
	assert.Equal(t, before, fileDigest(t, path), "read-only compatibility must not apply migration 002")
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestRedact_MasksLinkAttributeValuesWithoutMutatingInput(t *testing.T) {
	orig := model.Trace{Spans: []model.Span{{Links: []model.SpanLink{{
		TraceID: "trace", SpanID: "span",
		Attributes: []model.Attribute{{Key: "token", ValueType: "str", Value: githubPAT}},
	}}}}}
	got, err := Redact(orig)
	require.NoError(t, err)

	assert.Equal(t, "‹redacted›", got.Spans[0].Links[0].Attributes[0].Value)
	assert.Equal(t, githubPAT, orig.Spans[0].Links[0].Attributes[0].Value)
}

func TestInsertTrace_RedactsLinkTraceStateBeforePersistence(t *testing.T) {
	st, ctx := newTestStore(t)
	secret := "sk-" + strings.Repeat("T", 24)
	original := minimalTrace("trace-state", "fixture", 1)
	original.Spans[0].Links = []model.SpanLink{{
		TraceID: "linked-trace", SpanID: "linked-span", TraceState: "vendor=" + secret,
	}}

	require.NoError(t, st.InsertTrace(ctx, original))
	got, err := st.GetTrace(ctx, original.TraceID)
	require.NoError(t, err)
	require.Len(t, got.Spans[0].Links, 1)
	assert.NotContains(t, got.Spans[0].Links[0].TraceState, secret)
	assert.Contains(t, got.Spans[0].Links[0].TraceState, "‹redacted›")
	assert.Contains(t, original.Spans[0].Links[0].TraceState, secret, "input must not be mutated")
}
