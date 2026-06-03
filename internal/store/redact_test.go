package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

// Test credentials are built from a prefix + repeated filler at runtime so no
// full credential pattern appears as a literal in source (which would trip
// secret scanners) while still exercising each redaction regex.
var (
	awsKey    = "AKIA" + strings.Repeat("Q", 16)
	githubPAT = "ghp_" + strings.Repeat("a", 36)
	openAIKey = "sk-" + strings.Repeat("B", 32)
	slackTok  = "xoxb-" + strings.Repeat("1", 24)
)

func TestRedactString_AllFourPatterns(t *testing.T) {
	for _, s := range []string{awsKey, githubPAT, openAIKey, slackTok} {
		got := redactString(s)
		assert.Equalf(t, redactionMask, got, "secret %q must be fully masked", s)
		assert.NotContainsf(t, got, s, "raw secret %q must not survive", s)
	}
}

func TestRedactString_MasksEmbeddedSecretAndKeepsContext(t *testing.T) {
	got := redactString("deploy with " + awsKey + " then exit")
	assert.Equal(t, "deploy with "+redactionMask+" then exit", got)
}

func TestRedactString_LeavesOrdinaryTextUntouched(t *testing.T) {
	for _, s := range []string{"make all", "go test ./...", "compile", "sk-too-short", ""} {
		assert.Equal(t, s, redactString(s), "non-secret text must pass through unchanged")
	}
}

func TestInsertTrace_RedactsBeforePersisting(t *testing.T) {
	st, ctx := newTestStore(t)

	tr := model.Trace{
		TraceID: "redact-1", RunLabel: "push " + githubPAT, Source: "mark", RootName: "push",
		StartedNs: 0, EndedNs: 100, DurationNs: 100, SpanCount: 1,
		Spans: []model.Span{{
			SpanID: "s1", TraceID: "redact-1", Name: "auth " + githubPAT,
			Kind: model.KindInternal, Status: model.StatusOk,
			StartedNs: 0, EndedNs: 100, DurationNs: 100,
			Attributes: []model.Attribute{{Key: "token", ValueType: "str", Value: githubPAT}},
		}},
	}
	require.NoError(t, st.InsertTrace(ctx, tr))

	got, err := st.GetTrace(ctx, "redact-1")
	require.NoError(t, err)

	assert.NotContains(t, got.RunLabel, githubPAT, "run label must be redacted on disk")
	assert.Contains(t, got.RunLabel, redactionMask)
	assert.NotContains(t, got.Spans[0].Name, githubPAT, "span name must be redacted on disk")
	assert.Equal(t, redactionMask, got.Spans[0].Attributes[0].Value, "attribute value must be redacted on disk")
}

func TestRedact_DoesNotMutateInput(t *testing.T) {
	orig := model.Trace{
		RunLabel: awsKey,
		Spans:    []model.Span{{Name: awsKey, Attributes: []model.Attribute{{Key: "k", Value: awsKey}}}},
	}
	_ = Redact(orig)

	// Redact must return a copy; the caller's trace is left intact.
	assert.Equal(t, awsKey, orig.RunLabel)
	assert.Equal(t, awsKey, orig.Spans[0].Name)
	assert.Equal(t, awsKey, orig.Spans[0].Attributes[0].Value)
}
