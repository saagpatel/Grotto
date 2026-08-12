package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/saagpatel/grotto/internal/model"
)

func TestExtract_OnlyWhitelistedExperimentalFields(t *testing.T) {
	span := model.Span{Attributes: []model.Attribute{
		{Key: AttrResponseID, Value: "resp_2"},
		{Key: AttrPreviousResponse, Value: "resp_1"},
		{Key: AttrOutputItemType, Value: "compaction"},
		{Key: AttrContextType, Value: "compaction"},
		{Key: AttrCompactThreshold, Value: "200000"},
		{Key: "gen_ai.input.messages", Value: "private content"},
	}}
	got := Extract(span)

	assert.Equal(t, "resp_2", got.ResponseID)
	assert.Equal(t, "resp_1", got.PreviousResponse)
	assert.True(t, got.Compacted)
	assert.True(t, got.CompactionArmed)
	assert.NotContains(t, got.Fields, "gen_ai.input.messages")
}

func TestExtract_ThresholdAloneDoesNotClaimCompaction(t *testing.T) {
	got := Extract(model.Span{Attributes: []model.Attribute{{Key: AttrCompactThreshold, Value: "1000"}}})
	assert.False(t, got.Compacted)
	assert.False(t, got.CompactionArmed)
}
