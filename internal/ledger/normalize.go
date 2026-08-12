package ledger

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

const (
	attrProvider      = "gen_ai.provider.name"
	attrRequestModel  = "gen_ai.request.model"
	attrResponseModel = "gen_ai.response.model"
	attrOperation     = "gen_ai.operation.name"
	attrToolName      = "gen_ai.tool.name"

	attrInput        = "gen_ai.usage.input_tokens"
	attrOutput       = "gen_ai.usage.output_tokens"
	attrCacheRead    = "gen_ai.usage.cache_read.input_tokens"
	attrCacheWrite   = "gen_ai.usage.cache_creation.input_tokens"
	attrReasoning    = "gen_ai.usage.reasoning.output_tokens"
	attrLegacyInput  = "gen_ai.usage.prompt_tokens"
	attrLegacyOutput = "gen_ai.usage.completion_tokens"

	attrOpenAIInput         = "openai.usage.input_tokens"
	attrOpenAIOutput        = "openai.usage.output_tokens"
	attrOpenAICacheRead     = "openai.usage.input_tokens_details.cached_tokens"
	attrOpenAIReasoning     = "openai.usage.output_tokens_details.reasoning_tokens"
	attrAnthropicInput      = "anthropic.usage.input_tokens"
	attrAnthropicOutput     = "anthropic.usage.output_tokens"
	attrAnthropicCacheRead  = "anthropic.usage.cache_read_input_tokens"
	attrAnthropicCacheWrite = "anthropic.usage.cache_creation_input_tokens"

	attrToolInput    = "grotto.usage.tool.input_tokens"
	attrContextLimit = "grotto.context.window.limit_tokens"
	attrUsageMode    = "grotto.usage.mode"
	attrUsageSeries  = "grotto.usage.series"
	attrRetryID      = "grotto.retry.logical_call_id"
	attrRetryAttempt = "grotto.retry.attempt"
)

type candidate struct {
	value   int64
	sources []Source
}

type rowState struct {
	row          Row
	usageBearing bool
}

// Build constructs a deterministic ledger report from the exact attributes
// already persisted on a trace. It performs no I/O.
func Build(tr model.Trace) Report {
	issues := make([]Issue, 0)
	spans := append([]model.Span(nil), tr.Spans...)
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].StartedNs != spans[j].StartedNs {
			return spans[i].StartedNs < spans[j].StartedNs
		}
		if spans[i].EndedNs != spans[j].EndedNs {
			return spans[i].EndedNs < spans[j].EndedNs
		}
		return spans[i].SpanID < spans[j].SpanID
	})

	depths, branches := spanPositions(spans, &issues)
	states := make([]rowState, 0, len(spans))
	limitCandidates := make([]candidate, 0)
	for _, sp := range spans {
		attrs := attributesByKey(sp.Attributes)
		observed, usageBearing := normalizeUsage(sp.SpanID, attrs, &issues)
		mode := stringAttr(attrs, attrUsageMode)
		if mode == "" {
			mode = "delta"
		}
		if mode != "delta" && mode != "cumulative" && mode != "rollup" {
			issues = append(issues, Issue{Code: "invalid_usage_mode", SpanID: sp.SpanID, Attribute: attrUsageMode, Message: fmt.Sprintf("unsupported usage mode %q", mode)})
			mode = "invalid"
		}
		series := stringAttr(attrs, attrUsageSeries)
		provider := stringAttr(attrs, attrProvider)
		modelName := stringAttr(attrs, attrResponseModel)
		if modelName == "" {
			modelName = stringAttr(attrs, attrRequestModel)
		}
		operation := stringAttr(attrs, attrOperation)
		toolCall := operation == "execute_tool" || stringAttr(attrs, attrToolName) != ""
		retry := normalizeRetry(sp.SpanID, attrs, &issues)

		if c, ok := parseCandidate(sp.SpanID, attrs, attrContextLimit, "grotto", &issues); ok {
			if c.value <= 0 {
				issues = append(issues, Issue{Code: "invalid_context_limit", SpanID: sp.SpanID, Attribute: attrContextLimit, Message: "context limit must be positive"})
			} else {
				limitCandidates = append(limitCandidates, c)
			}
		}

		states = append(states, rowState{row: Row{
			SpanID: sp.SpanID, ParentSpanID: sp.ParentSpanID, Name: sp.Name,
			StartedNs: sp.StartedNs, EndedNs: sp.EndedNs,
			Depth: depths[sp.SpanID], BranchSpanID: branches[sp.SpanID],
			Provider: provider, Model: modelName, Operation: operation,
			Mode: mode, Series: series, ToolCall: toolCall, Retry: retry,
			Observed: observed,
		}, usageBearing: usageBearing})
	}

	applyContributionModes(states, &issues)
	summary := summarize(states, limitCandidates, &issues)
	reconcileRollups(states, &issues)
	sortIssues(issues)

	rows := make([]Row, len(states))
	for i := range states {
		rows[i] = states[i].row
	}
	return Report{
		Schema:  ReportSchema,
		Semconv: SemconvSnapshot{Status: "development", Core: "v1.44.0", GenAIRef: "8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958"},
		TraceID: tr.TraceID, RunLabel: tr.RunLabel, Source: tr.Source,
		Summary: summary, Rows: rows, Issues: issues,
	}
}

func attributesByKey(attrs []model.Attribute) map[string]model.Attribute {
	out := make(map[string]model.Attribute, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr
	}
	return out
}

func stringAttr(attrs map[string]model.Attribute, key string) string {
	a, ok := attrs[key]
	if !ok || a.ValueType != "str" {
		return ""
	}
	return a.Value
}

func normalizeRetry(spanID string, attrs map[string]model.Attribute, issues *[]Issue) RetryAttribution {
	retry := RetryAttribution{LogicalCallID: stringAttr(attrs, attrRetryID)}
	if _, exists := attrs[attrRetryAttempt]; !exists {
		return retry
	}
	c, ok := parseCandidate(spanID, attrs, attrRetryAttempt, "grotto", issues)
	if !ok || c.value <= 0 {
		*issues = append(*issues, Issue{Code: "invalid_retry_attempt", SpanID: spanID, Attribute: attrRetryAttempt, Message: "retry attempt must be a positive integer"})
		return retry
	}
	retry.Attempt = int64Pointer(c.value)
	return retry
}

func normalizeUsage(spanID string, attrs map[string]model.Attribute, issues *[]Issue) (Usage, bool) {
	provider := stringAttr(attrs, attrProvider)
	recognized := []string{attrInput, attrOutput, attrCacheRead, attrCacheWrite, attrReasoning, attrLegacyInput, attrLegacyOutput, attrToolInput}
	if provider == "openai" {
		recognized = append(recognized, attrOpenAIInput, attrOpenAIOutput, attrOpenAICacheRead, attrOpenAIReasoning)
	}
	if provider == "anthropic" {
		recognized = append(recognized, attrAnthropicInput, attrAnthropicOutput, attrAnthropicCacheRead, attrAnthropicCacheWrite)
	}
	usageBearing := false
	for _, key := range recognized {
		if _, ok := attrs[key]; ok {
			usageBearing = true
			break
		}
	}

	input := collectCandidates(spanID, attrs, []keyFamily{{attrInput, "otel"}, {attrLegacyInput, "otel_deprecated"}}, issues)
	output := collectCandidates(spanID, attrs, []keyFamily{{attrOutput, "otel"}, {attrLegacyOutput, "otel_deprecated"}}, issues)
	cacheRead := collectCandidates(spanID, attrs, []keyFamily{{attrCacheRead, "otel"}}, issues)
	cacheWrite := collectCandidates(spanID, attrs, []keyFamily{{attrCacheWrite, "otel"}}, issues)
	reasoning := collectCandidates(spanID, attrs, []keyFamily{{attrReasoning, "otel"}}, issues)
	toolInput := collectCandidates(spanID, attrs, []keyFamily{{attrToolInput, "grotto"}}, issues)

	switch provider {
	case "openai":
		input = append(input, collectCandidates(spanID, attrs, []keyFamily{{attrOpenAIInput, "openai"}}, issues)...)
		output = append(output, collectCandidates(spanID, attrs, []keyFamily{{attrOpenAIOutput, "openai"}}, issues)...)
		cacheRead = append(cacheRead, collectCandidates(spanID, attrs, []keyFamily{{attrOpenAICacheRead, "openai"}}, issues)...)
		reasoning = append(reasoning, collectCandidates(spanID, attrs, []keyFamily{{attrOpenAIReasoning, "openai"}}, issues)...)
	case "anthropic":
		output = append(output, collectCandidates(spanID, attrs, []keyFamily{{attrAnthropicOutput, "anthropic"}}, issues)...)
		cacheRead = append(cacheRead, collectCandidates(spanID, attrs, []keyFamily{{attrAnthropicCacheRead, "anthropic"}}, issues)...)
		cacheWrite = append(cacheWrite, collectCandidates(spanID, attrs, []keyFamily{{attrAnthropicCacheWrite, "anthropic"}}, issues)...)
		if derived, ok := anthropicInputCandidate(spanID, attrs, issues); ok {
			input = append(input, derived)
		}
	}

	u := Usage{
		Input:      resolveCount("input", spanID, input, issues),
		Output:     resolveCount("output", spanID, output, issues),
		CacheRead:  resolveCount("cache_read", spanID, cacheRead, issues),
		CacheWrite: resolveCount("cache_write", spanID, cacheWrite, issues),
		Reasoning:  resolveCount("reasoning", spanID, reasoning, issues),
		ToolInput:  resolveCount("tool_input", spanID, toolInput, issues),
	}
	u.CacheRead = enforceSubset(spanID, "cache_read_exceeds_input", u.CacheRead, u.Input, issues)
	u.CacheWrite = enforceSubset(spanID, "cache_write_exceeds_input", u.CacheWrite, u.Input, issues)
	if isKnown(u.CacheRead) && isKnown(u.CacheWrite) && isKnown(u.Input) {
		cacheTotal, overflow := checkedAdd(*u.CacheRead.Value, *u.CacheWrite.Value)
		if overflow || cacheTotal > *u.Input.Value {
			*issues = append(*issues, Issue{Code: "cache_subsets_exceed_input", SpanID: spanID, Message: "cache-read plus cache-write exceeds input total"})
			u.CacheRead.Status, u.CacheRead.Value, u.CacheRead.Reason = "unknown", nil, "impossible_partition"
			u.CacheWrite.Status, u.CacheWrite.Value, u.CacheWrite.Reason = "unknown", nil, "impossible_partition"
		}
	}
	u.Reasoning = enforceSubset(spanID, "reasoning_exceeds_output", u.Reasoning, u.Output, issues)
	u.ToolInput = enforceSubset(spanID, "tool_input_exceeds_input", u.ToolInput, u.Input, issues)
	u.Total = addCounts(u.Input, u.Output, "input_or_output_unknown", spanID, issues)
	return u, usageBearing
}

type keyFamily struct{ key, family string }

func collectCandidates(spanID string, attrs map[string]model.Attribute, keys []keyFamily, issues *[]Issue) []candidate {
	out := make([]candidate, 0, len(keys))
	for _, item := range keys {
		if c, ok := parseCandidate(spanID, attrs, item.key, item.family, issues); ok {
			out = append(out, c)
		}
	}
	return out
}

func parseCandidate(spanID string, attrs map[string]model.Attribute, key, family string, issues *[]Issue) (candidate, bool) {
	a, ok := attrs[key]
	if !ok {
		return candidate{}, false
	}
	source := Source{Attribute: key, Value: a.Value, ValueType: a.ValueType, Family: family}
	if a.ValueType != "int" {
		*issues = append(*issues, Issue{Code: "invalid_attribute_type", SpanID: spanID, Attribute: key, Message: fmt.Sprintf("token count requires int, got %q", a.ValueType)})
		return candidate{}, false
	}
	value, err := strconv.ParseInt(a.Value, 10, 64)
	if err != nil {
		*issues = append(*issues, Issue{Code: "invalid_integer", SpanID: spanID, Attribute: key, Message: "token count is not a signed 64-bit integer"})
		return candidate{}, false
	}
	if value < 0 {
		*issues = append(*issues, Issue{Code: "negative_count", SpanID: spanID, Attribute: key, Message: "token count cannot be negative"})
		return candidate{}, false
	}
	return candidate{value: value, sources: []Source{source}}, true
}

func anthropicInputCandidate(spanID string, attrs map[string]model.Attribute, issues *[]Issue) (candidate, bool) {
	base, ok := parseCandidate(spanID, attrs, attrAnthropicInput, "anthropic", issues)
	if !ok {
		return candidate{}, false
	}
	value := base.value
	sources := append([]Source(nil), base.sources...)
	for _, key := range []string{attrAnthropicCacheRead, attrAnthropicCacheWrite} {
		if _, present := attrs[key]; !present {
			continue
		}
		part, valid := parseCandidate(spanID, attrs, key, "anthropic", issues)
		if !valid {
			return candidate{}, false
		}
		var overflow bool
		value, overflow = checkedAdd(value, part.value)
		if overflow {
			*issues = append(*issues, Issue{Code: "count_overflow", SpanID: spanID, Attribute: attrAnthropicInput, Message: "Anthropic input plus cache tokens exceeds int64"})
			return candidate{}, false
		}
		sources = append(sources, part.sources...)
	}
	for i := range sources {
		sources[i].Derived = "anthropic input + cache read + cache creation"
	}
	return candidate{value: value, sources: sources}, true
}

func resolveCount(signal, spanID string, candidates []candidate, issues *[]Issue) Count {
	if len(candidates) == 0 {
		return unknownCount("missing")
	}
	allSources := make([]Source, 0)
	values := make(map[int64]struct{})
	for _, c := range candidates {
		values[c.value] = struct{}{}
		allSources = append(allSources, c.sources...)
	}
	sortSources(allSources)
	if len(values) != 1 {
		*issues = append(*issues, Issue{Code: "conflicting_usage", SpanID: spanID, Message: signal + " candidates disagree"})
		return Count{Status: "unknown", Value: nil, Reason: "conflict", Sources: allSources}
	}
	value := candidates[0].value
	if len(candidates) > 1 {
		*issues = append(*issues, Issue{Code: "duplicate_usage", SpanID: spanID, Message: signal + " is reported by multiple equal sources and counted once"})
	}
	return knownCount(value, allSources)
}

func enforceSubset(spanID, code string, subset, total Count, issues *[]Issue) Count {
	if !isKnown(subset) || !isKnown(total) || *subset.Value <= *total.Value {
		return subset
	}
	*issues = append(*issues, Issue{Code: code, SpanID: spanID, Message: "subset token count exceeds its containing total"})
	subset.Status = "unknown"
	subset.Value = nil
	subset.Reason = "impossible_subset"
	return subset
}

func applyContributionModes(states []rowState, issues *[]Issue) {
	previous := make(map[string]Usage)
	for i := range states {
		row := &states[i].row
		switch row.Mode {
		case "delta":
			row.Contribution = row.Observed
			row.Contributes = states[i].usageBearing
		case "cumulative":
			if row.Series == "" {
				row.Contribution = unknownUsage("missing_cumulative_series")
				row.Contributes = false
				*issues = append(*issues, Issue{Code: "missing_cumulative_series", SpanID: row.SpanID, Attribute: attrUsageSeries, Message: "cumulative usage requires a series identifier"})
				continue
			}
			key := row.Provider + "\x00" + row.Model + "\x00" + row.Series
			prior, seen := previous[key]
			if !seen {
				prior = zeroUsage()
			}
			row.Contribution = subtractUsage(row.SpanID, row.Observed, prior, issues)
			row.Contributes = states[i].usageBearing
			previous[key] = row.Observed
		case "rollup":
			row.Contribution = unknownUsage("rollup_excluded")
			row.Contributes = false
		default:
			row.Contribution = unknownUsage("invalid_usage_mode")
			row.Contributes = false
		}
	}
}

func subtractUsage(spanID string, current, prior Usage, issues *[]Issue) Usage {
	out := Usage{
		Input:      subtractCount(spanID, "input", current.Input, prior.Input, issues),
		Output:     subtractCount(spanID, "output", current.Output, prior.Output, issues),
		CacheRead:  subtractCount(spanID, "cache_read", current.CacheRead, prior.CacheRead, issues),
		CacheWrite: subtractCount(spanID, "cache_write", current.CacheWrite, prior.CacheWrite, issues),
		Reasoning:  subtractCount(spanID, "reasoning", current.Reasoning, prior.Reasoning, issues),
		ToolInput:  subtractCount(spanID, "tool_input", current.ToolInput, prior.ToolInput, issues),
	}
	out.Total = addCounts(out.Input, out.Output, "input_or_output_unknown", spanID, issues)
	return out
}

func subtractCount(spanID, signal string, current, prior Count, issues *[]Issue) Count {
	if !isKnown(current) {
		return Count{Status: "unknown", Reason: current.Reason, Sources: current.Sources}
	}
	if !isKnown(prior) {
		return Count{Status: "unknown", Reason: "previous_cumulative_unknown", Sources: current.Sources}
	}
	if *current.Value < *prior.Value {
		*issues = append(*issues, Issue{Code: "cumulative_decrease", SpanID: spanID, Message: signal + " cumulative count decreased"})
		return Count{Status: "unknown", Reason: "cumulative_decrease", Sources: current.Sources}
	}
	return knownCount(*current.Value-*prior.Value, current.Sources)
}

func summarize(states []rowState, limits []candidate, issues *[]Issue) Summary {
	summary := Summary{SpanCount: len(states)}
	for i := range states {
		if states[i].usageBearing {
			summary.UsageRows++
		}
		if states[i].row.ToolCall {
			summary.ToolCalls++
		}
		if states[i].row.Retry.Attempt != nil {
			summary.RetryAttempts++
		}
	}
	summary.Usage = Usage{
		Input:      aggregateSignal(states, func(u Usage) Count { return u.Input }, "input", issues),
		Output:     aggregateSignal(states, func(u Usage) Count { return u.Output }, "output", issues),
		CacheRead:  aggregateSignal(states, func(u Usage) Count { return u.CacheRead }, "cache_read", issues),
		CacheWrite: aggregateSignal(states, func(u Usage) Count { return u.CacheWrite }, "cache_write", issues),
		Reasoning:  aggregateSignal(states, func(u Usage) Count { return u.Reasoning }, "reasoning", issues),
		ToolInput:  aggregateSignal(states, func(u Usage) Count { return u.ToolInput }, "tool_input", issues),
	}
	summary.Usage.Total = addCounts(summary.Usage.Input, summary.Usage.Output, "input_or_output_unknown", "", issues)
	summary.Context = contextPressure(limits, summary.Usage.Total, issues)
	return summary
}

func aggregateSignal(states []rowState, selectCount func(Usage) Count, signal string, issues *[]Issue) Count {
	var total int64
	var sources []Source
	seen := false
	unknown := false
	for _, state := range states {
		if !state.row.Contributes || !state.usageBearing {
			continue
		}
		seen = true
		count := selectCount(state.row.Contribution)
		if !isKnown(count) {
			unknown = true
			sources = append(sources, count.Sources...)
			continue
		}
		var overflow bool
		total, overflow = checkedAdd(total, *count.Value)
		if overflow {
			*issues = append(*issues, Issue{Code: "count_overflow", Message: signal + " trace aggregate exceeds int64"})
			return Count{Status: "unknown", Reason: "overflow", Sources: sources}
		}
		sources = append(sources, count.Sources...)
	}
	if seen {
		sortSources(sources)
		if unknown {
			return Count{Status: "unknown", Reason: "incomplete_contributions", Sources: sources}
		}
		return knownCount(total, sources)
	}

	rollups := make([]candidate, 0)
	for _, state := range states {
		if state.row.Mode != "rollup" {
			continue
		}
		count := selectCount(state.row.Observed)
		if isKnown(count) {
			rollups = append(rollups, candidate{value: *count.Value, sources: count.Sources})
		}
	}
	if len(rollups) == 0 {
		return unknownCount("no_observations")
	}
	return resolveCount(signal+" rollup", "", rollups, issues)
}

func contextPressure(limits []candidate, used Count, issues *[]Issue) ContextPressure {
	limit := resolveCount("context_limit", "", limits, issues)
	ctx := ContextPressure{Status: "unknown", Limit: limit, Used: used, Reason: "context_limit_or_usage_unknown"}
	if !isKnown(limit) || !isKnown(used) {
		return ctx
	}
	ratio := float64(*used.Value) / float64(*limit.Value)
	ctx.Status, ctx.Ratio, ctx.Reason = "known", &ratio, ""
	return ctx
}

func reconcileRollups(states []rowState, issues *[]Issue) {
	byID := make(map[string]Row, len(states))
	for _, state := range states {
		byID[state.row.SpanID] = state.row
	}
	type signalSelector struct {
		name string
		fn   func(Usage) Count
	}
	signals := []signalSelector{
		{"input", func(u Usage) Count { return u.Input }},
		{"output", func(u Usage) Count { return u.Output }},
		{"cache_read", func(u Usage) Count { return u.CacheRead }},
		{"cache_write", func(u Usage) Count { return u.CacheWrite }},
		{"reasoning", func(u Usage) Count { return u.Reasoning }},
		{"tool_input", func(u Usage) Count { return u.ToolInput }},
	}
	for _, state := range states {
		if state.row.Mode != "rollup" {
			continue
		}
		for _, signal := range signals {
			expected := signal.fn(state.row.Observed)
			if !isKnown(expected) {
				continue
			}
			var actual int64
			seen, complete := false, true
			for _, child := range states {
				if !child.row.Contributes || !child.usageBearing || !isDescendant(child.row.SpanID, state.row.SpanID, byID) {
					continue
				}
				seen = true
				count := signal.fn(child.row.Contribution)
				if !isKnown(count) {
					complete = false
					continue
				}
				var overflow bool
				actual, overflow = checkedAdd(actual, *count.Value)
				if overflow {
					complete = false
				}
			}
			if seen && complete && actual != *expected.Value {
				*issues = append(*issues, Issue{Code: "unexplained_delta", SpanID: state.row.SpanID, Message: fmt.Sprintf("%s rollup=%d descendant_contributions=%d delta=%d", signal.name, *expected.Value, actual, *expected.Value-actual)})
			}
		}
	}
}

func spanPositions(spans []model.Span, issues *[]Issue) (map[string]int, map[string]string) {
	byID := make(map[string]model.Span, len(spans))
	for _, sp := range spans {
		byID[sp.SpanID] = sp
	}
	depths := make(map[string]int, len(spans))
	branches := make(map[string]string, len(spans))
	state := make(map[string]int, len(spans))
	var visit func(string) (int, string)
	visit = func(id string) (int, string) {
		if state[id] == 2 {
			return depths[id], branches[id]
		}
		if state[id] == 1 {
			*issues = append(*issues, Issue{Code: "parent_cycle", SpanID: id, Message: "span parent chain contains a cycle"})
			depths[id], branches[id], state[id] = 0, id, 2
			return 0, id
		}
		state[id] = 1
		sp := byID[id]
		if sp.ParentSpanID == "" {
			depths[id], branches[id], state[id] = 0, "", 2
			return 0, ""
		}
		if _, ok := byID[sp.ParentSpanID]; !ok {
			*issues = append(*issues, Issue{Code: "orphan_span", SpanID: id, Message: "parent span is absent; row remains visible"})
			depths[id], branches[id], state[id] = 0, id, 2
			return 0, id
		}
		parentDepth, parentBranch := visit(sp.ParentSpanID)
		depths[id] = parentDepth + 1
		if parentDepth == 0 {
			branches[id] = id
		} else {
			branches[id] = parentBranch
		}
		state[id] = 2
		return depths[id], branches[id]
	}
	for _, sp := range spans {
		visit(sp.SpanID)
	}
	return depths, branches
}

func isDescendant(id, ancestor string, rows map[string]Row) bool {
	seen := make(map[string]struct{})
	current, ok := rows[id]
	for ok && current.ParentSpanID != "" {
		if current.ParentSpanID == ancestor {
			return true
		}
		if _, exists := seen[current.ParentSpanID]; exists {
			return false
		}
		seen[current.ParentSpanID] = struct{}{}
		current, ok = rows[current.ParentSpanID]
	}
	return false
}

func zeroUsage() Usage {
	zero := knownCount(0, nil)
	return Usage{Input: zero, Output: zero, CacheRead: zero, CacheWrite: zero, Reasoning: zero, ToolInput: zero, Total: zero}
}

func unknownUsage(reason string) Usage {
	unknown := unknownCount(reason)
	return Usage{Input: unknown, Output: unknown, CacheRead: unknown, CacheWrite: unknown, Reasoning: unknown, ToolInput: unknown, Total: unknown}
}

func addCounts(a, b Count, reason, spanID string, issues *[]Issue) Count {
	if !isKnown(a) || !isKnown(b) {
		return Count{Status: "unknown", Reason: reason, Sources: append(append([]Source(nil), a.Sources...), b.Sources...)}
	}
	value, overflow := checkedAdd(*a.Value, *b.Value)
	if overflow {
		*issues = append(*issues, Issue{Code: "count_overflow", SpanID: spanID, Message: "input plus output exceeds int64"})
		return Count{Status: "unknown", Reason: "overflow", Sources: append(append([]Source(nil), a.Sources...), b.Sources...)}
	}
	sources := append(append([]Source(nil), a.Sources...), b.Sources...)
	sortSources(sources)
	return knownCount(value, sources)
}

func checkedAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}

func knownCount(value int64, sources []Source) Count {
	return Count{Status: "known", Value: int64Pointer(value), Sources: append([]Source(nil), sources...)}
}

func unknownCount(reason string) Count { return Count{Status: "unknown", Value: nil, Reason: reason} }

func isKnown(count Count) bool { return count.Status == "known" && count.Value != nil }

func int64Pointer(value int64) *int64 { return &value }

func sortSources(sources []Source) {
	sort.Slice(sources, func(i, j int) bool {
		left := strings.Join([]string{sources[i].Attribute, sources[i].Value, sources[i].Family}, "\x00")
		right := strings.Join([]string{sources[j].Attribute, sources[j].Value, sources[j].Family}, "\x00")
		return left < right
	})
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		left := strings.Join([]string{issues[i].Code, issues[i].SpanID, issues[i].Attribute, issues[i].Message}, "\x00")
		right := strings.Join([]string{issues[j].Code, issues[j].SpanID, issues[j].Attribute, issues[j].Message}, "\x00")
		return left < right
	})
}
