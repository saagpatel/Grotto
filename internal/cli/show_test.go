package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/ledger"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

// TestWithoutSections drops only the spans carrying the cargo.section marker,
// leaving crates and non-cargo spans untouched.
func TestWithoutSections(t *testing.T) {
	spans := []model.Span{
		{SpanID: "root", Name: "cargo"},
		{SpanID: "crate", Name: "serde v1", Attributes: []model.Attribute{{Key: "cargo.unit", Value: "0"}}},
		{SpanID: "fe", Name: "frontend", Attributes: []model.Attribute{{Key: adapter.AttrSection, Value: "frontend"}}},
		{SpanID: "cg", Name: "codegen", Attributes: []model.Attribute{{Key: adapter.AttrSection, Value: "codegen"}}},
	}

	got := withoutSections(spans)
	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2 (root + crate; sections dropped)", len(got))
	}
	for _, s := range got {
		if hasAttr(s, adapter.AttrSection) {
			t.Errorf("span %q is a section and should have been dropped", s.Name)
		}
	}
}

func TestShowLedgerTextAndJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "grotto.db")
	t.Setenv("GROTTO_DB", dbPath)
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	require.NoError(t, err)
	tr := model.Trace{
		TraceID: "ledger-cli", RunLabel: "ledger cli", Source: "otlp", RootName: "chat",
		StartedNs: 1, EndedNs: 2, DurationNs: 1, SpanCount: 1,
		Spans: []model.Span{{
			SpanID: "span", TraceID: "ledger-cli", Name: "chat", StartedNs: 1, EndedNs: 2, DurationNs: 1,
			Attributes: []model.Attribute{
				{Key: "gen_ai.usage.input_tokens", ValueType: "int", Value: "4"},
				{Key: "gen_ai.usage.output_tokens", ValueType: "int", Value: "2"},
				{Key: "gen_ai.usage.cache_read.input_tokens", ValueType: "int", Value: "0"},
				{Key: "gen_ai.usage.cache_creation.input_tokens", ValueType: "int", Value: "0"},
				{Key: "gen_ai.usage.reasoning.output_tokens", ValueType: "int", Value: "0"},
				{Key: "grotto.usage.tool.input_tokens", ValueType: "int", Value: "0"},
			},
		}},
	}
	require.NoError(t, st.InsertTrace(ctx, tr))
	require.NoError(t, st.Close())

	textCmd := newShowCmd()
	var textOut bytes.Buffer
	textCmd.SetOut(&textOut)
	textCmd.SetArgs([]string{"ledger-cli", "--ledger"})
	require.NoError(t, textCmd.Execute())
	assert.Contains(t, textOut.String(), "cache/context ledger v1")
	assert.Contains(t, textOut.String(), "input=4 output=2 total=6")

	jsonCmd := newShowCmd()
	var jsonOut bytes.Buffer
	jsonCmd.SetOut(&jsonOut)
	jsonCmd.SetArgs([]string{"ledger-cli", "--ledger-json"})
	require.NoError(t, jsonCmd.Execute())
	var report ledger.Report
	require.NoError(t, json.Unmarshal(jsonOut.Bytes(), &report))
	assert.Equal(t, ledger.ReportSchema, report.Schema)
	require.NoError(t, ledger.ValidateReport(report))
}

func TestShowLedgerFlagValidation(t *testing.T) {
	cmd := newShowCmd()
	cmd.SetArgs([]string{"trace", "--ledger-rates", "rates.json"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "requires --ledger"))

	mutual := newShowCmd()
	mutual.SetArgs([]string{"trace", "--json", "--ledger"})
	err = mutual.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none of the others can be")
}
