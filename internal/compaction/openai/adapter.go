// Package openai isolates the optional OpenAI Responses fallback used by the
// Compaction X-Ray. Its attribute names are an experimental Grotto transport
// contract, not OpenTelemetry semantic conventions.
package openai

import "github.com/saagpatel/grotto/internal/model"

// Experimental Grotto attributes whose values mirror official OpenAI Responses
// fields. Standard gen_ai.* attributes always take precedence in the core.
const (
	AttrResponseID       = "grotto.openai.response.id"
	AttrPreviousResponse = "grotto.openai.previous_response_id"
	AttrOutputItemType   = "grotto.openai.output_item.type"
	AttrContextType      = "grotto.openai.context_management.type"
	AttrCompactThreshold = "grotto.openai.context_management.compact_threshold"
)

// Signals is the narrow provider-specific input understood by the core.
type Signals struct {
	ResponseID       string
	PreviousResponse string
	Compacted        bool
	CompactionArmed  bool
	Fields           []string
}

// Extract reads only the documented experimental adapter attributes. It never
// inspects prompts, messages, tool arguments, or output content.
func Extract(span model.Span) Signals {
	values := make(map[string]string)
	for _, attr := range span.Attributes {
		switch attr.Key {
		case AttrResponseID, AttrPreviousResponse, AttrOutputItemType,
			AttrContextType, AttrCompactThreshold:
			if _, exists := values[attr.Key]; !exists {
				values[attr.Key] = attr.Value
			}
		}
	}

	var out Signals
	if value := values[AttrResponseID]; value != "" {
		out.ResponseID = value
		out.Fields = append(out.Fields, AttrResponseID)
	}
	if value := values[AttrPreviousResponse]; value != "" {
		out.PreviousResponse = value
		out.Fields = append(out.Fields, AttrPreviousResponse)
	}
	if values[AttrOutputItemType] == "compaction" {
		out.Compacted = true
		out.Fields = append(out.Fields, AttrOutputItemType)
	}
	if values[AttrContextType] == "compaction" {
		out.CompactionArmed = true
		out.Fields = append(out.Fields, AttrContextType)
		if _, ok := values[AttrCompactThreshold]; ok {
			out.Fields = append(out.Fields, AttrCompactThreshold)
		}
	}
	return out
}
