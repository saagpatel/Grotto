// Package compaction normalizes content-free compaction and response-chain
// observations from genuine OpenTelemetry GenAI spans.
package compaction

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/saagpatel/grotto/internal/compaction/openai"
	"github.com/saagpatel/grotto/internal/model"
)

// SchemaVersion identifies the deterministic machine-readable report contract.
const SchemaVersion = "grotto.compaction_report.v1"

const (
	attrOperation        = "gen_ai.operation.name"
	attrProvider         = "gen_ai.provider.name"
	attrConversation     = "gen_ai.conversation.id"
	attrCompacted        = "gen_ai.conversation.compacted"
	attrPreviousResponse = "gen_ai.request.previous_response.id"
	attrResponseID       = "gen_ai.response.id"
	attrInputTokens      = "gen_ai.usage.input_tokens"
	attrOutputTokens     = "gen_ai.usage.output_tokens"

	attrAnswerLabel       = "grotto.compaction.answer.label"
	attrAnswerHash        = "grotto.compaction.answer.hash"
	attrAnswerFingerprint = "grotto.compaction.answer.fingerprint"
)

// Report is a deterministic, content-free view of one trace.
type Report struct {
	Schema              string        `json:"schema"`
	TraceID             string        `json:"trace_id"`
	SourceSpanCount     int           `json:"source_span_count"`
	SemanticConventions Standards     `json:"semantic_conventions"`
	Observations        []Observation `json:"observations"`
	Warnings            []Warning     `json:"warnings,omitempty"`
	ClaimCeiling        string        `json:"claim_ceiling"`
}

// Standards records the exact experimental standard binding used by the report.
type Standards struct {
	Family string `json:"family"`
	Status string `json:"status"`
	AsOf   string `json:"as_of"`
	Commit string `json:"commit"`
}

// Observation is one ordered GenAI response operation.
type Observation struct {
	Sequence       int              `json:"sequence"`
	SpanID         string           `json:"span_id"`
	StartedNs      int64            `json:"started_ns"`
	Operation      string           `json:"operation,omitempty"`
	Provider       string           `json:"provider,omitempty"`
	ConversationID string           `json:"conversation_id,omitempty"`
	Chain          ChainObservation `json:"chain"`
	Compaction     Boundary         `json:"compaction"`
	Tokens         TokenObservation `json:"tokens"`
	ContextReset   Indicator        `json:"context_reset"`
	AnswerDrift    DriftIndicator   `json:"answer_drift"`
	Provenance     []Provenance     `json:"provenance,omitempty"`
}

// ChainObservation describes response ancestry without fabricating missing IDs.
type ChainObservation struct {
	ResponseID         string `json:"response_id,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	LinkedSpanID       string `json:"linked_span_id,omitempty"`
	Status             string `json:"status"`
}

// Boundary describes positive compaction evidence and configured-only state.
type Boundary struct {
	State string `json:"state"`
	Armed bool   `json:"armed"`
}

// OptionalInt preserves known/unknown numeric state without sentinel values.
type OptionalInt struct {
	State string `json:"state"`
	Value *int64 `json:"value,omitempty"`
}

// TokenDelta shows values immediately before and after the current boundary.
type TokenDelta struct {
	Before OptionalInt `json:"before"`
	After  OptionalInt `json:"after"`
	Delta  OptionalInt `json:"delta"`
}

// TokenObservation contains current values and boundary-local deltas only.
type TokenObservation struct {
	Input       OptionalInt `json:"input"`
	Output      OptionalInt `json:"output"`
	InputShift  TokenDelta  `json:"input_shift"`
	OutputShift TokenDelta  `json:"output_shift"`
}

// Indicator is a structural yes/no/unknown signal with a non-semantic reason.
type Indicator struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// DriftIndicator compares only caller-supplied structural values.
type DriftIndicator struct {
	State string `json:"state"`
	Kind  string `json:"kind,omitempty"`
	Note  string `json:"note"`
}

// Provenance binds a normalized signal to its source field.
type Provenance struct {
	Signal       string `json:"signal"`
	Source       string `json:"source"`
	Field        string `json:"field"`
	Experimental bool   `json:"experimental,omitempty"`
}

// Warning is a deterministic malformed, contradictory, or incomplete-data note.
type Warning struct {
	Code    string `json:"code"`
	SpanID  string `json:"span_id,omitempty"`
	Message string `json:"message"`
}

type workingObservation struct {
	Observation
	span        model.Span
	fingerprint string
	fpKind      string
}

// Analyze builds a deterministic report. Missing evidence remains UNKNOWN, and
// message/content attributes are never inspected.
func Analyze(trace model.Trace) Report {
	report := Report{
		Schema:          SchemaVersion,
		TraceID:         trace.TraceID,
		SourceSpanCount: len(trace.Spans),
		SemanticConventions: Standards{
			Family: "OpenTelemetry GenAI", Status: "development",
			AsOf: "2026-08-11", Commit: "8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958",
		},
		ClaimCeiling: "structural indicators only; no semantic-quality claim",
	}

	work := make([]workingObservation, 0)
	for _, span := range trace.Spans {
		item, warnings, ok := normalizeSpan(trace.TraceID, span)
		report.Warnings = append(report.Warnings, warnings...)
		if ok {
			work = append(work, item)
		}
	}
	if len(work) == 0 {
		report.Warnings = append(report.Warnings, Warning{Code: "no_gen_ai_observations", Message: "trace contains no recognized GenAI response operations"})
		return report
	}

	work, orderingWarnings := orderObservations(work)
	report.Warnings = append(report.Warnings, orderingWarnings...)
	indexByResponse := make(map[string]int)
	children := make(map[string]int)
	for i := range work {
		if id := work[i].Chain.ResponseID; id != "" {
			if _, exists := indexByResponse[id]; exists {
				report.Warnings = append(report.Warnings, Warning{Code: "duplicate_response_id", SpanID: work[i].SpanID, Message: "response id is not unique; ancestry is ambiguous"})
			} else {
				indexByResponse[id] = i
			}
		}
		if prev := work[i].Chain.PreviousResponseID; prev != "" {
			children[prev]++
		}
	}

	for i := range work {
		resolveContinuity(trace.TraceID, &work[i], work, indexByResponse, children, &report.Warnings)
		var previous *workingObservation
		if prevIndex, ok := indexByResponse[work[i].Chain.PreviousResponseID]; ok && prevIndex != i {
			previous = &work[prevIndex]
		}
		work[i].Tokens.InputShift = tokenDelta(previousToken(previous, true), work[i].Tokens.Input)
		work[i].Tokens.OutputShift = tokenDelta(previousToken(previous, false), work[i].Tokens.Output)
		work[i].ContextReset = contextReset(work[i], previous)
		work[i].AnswerDrift = drift(work[i], previous)
		work[i].Sequence = i + 1
		report.Observations = append(report.Observations, work[i].Observation)
	}

	sort.SliceStable(report.Warnings, func(i, j int) bool {
		if report.Warnings[i].SpanID != report.Warnings[j].SpanID {
			return report.Warnings[i].SpanID < report.Warnings[j].SpanID
		}
		if report.Warnings[i].Code != report.Warnings[j].Code {
			return report.Warnings[i].Code < report.Warnings[j].Code
		}
		return report.Warnings[i].Message < report.Warnings[j].Message
	})
	return report
}

func normalizeSpan(traceID string, span model.Span) (workingObservation, []Warning, bool) {
	attrs := firstAttributes(span.Attributes)
	providerSignals := openai.Extract(span)
	recognized := attrs[attrOperation] != "" || attrs[attrResponseID] != "" ||
		attrs[attrPreviousResponse] != "" || attrs[attrCompacted] != "" || len(providerSignals.Fields) > 0
	if !recognized {
		return workingObservation{}, nil, false
	}

	item := workingObservation{span: span}
	item.SpanID = span.SpanID
	item.StartedNs = span.StartedNs
	item.Operation = attrs[attrOperation]
	item.Provider = attrs[attrProvider]
	item.ConversationID = attrs[attrConversation]
	item.Chain.ResponseID = attrs[attrResponseID]
	item.Chain.PreviousResponseID = attrs[attrPreviousResponse]
	item.Chain.Status = "unknown"
	item.Compaction.State = "unknown"
	item.ContextReset = Indicator{State: "unknown", Reason: "compaction boundary or token evidence is incomplete"}
	item.AnswerDrift = DriftIndicator{State: "unknown", Note: "requires matching supplied synthetic labels, hashes, or structural fingerprints"}

	var warnings []Warning
	addStandardProvenance(&item, attrs)
	if providerSignals.ResponseID != "" {
		if item.Chain.ResponseID == "" {
			item.Chain.ResponseID = providerSignals.ResponseID
		} else if item.Chain.ResponseID != providerSignals.ResponseID {
			warnings = append(warnings, Warning{Code: "provider_conflict", SpanID: span.SpanID, Message: "standard and OpenAI adapter response ids disagree"})
		}
	}
	if providerSignals.PreviousResponse != "" {
		if item.Chain.PreviousResponseID == "" {
			item.Chain.PreviousResponseID = providerSignals.PreviousResponse
		} else if item.Chain.PreviousResponseID != providerSignals.PreviousResponse {
			warnings = append(warnings, Warning{Code: "provider_conflict", SpanID: span.SpanID, Message: "standard and OpenAI adapter previous response ids disagree"})
		}
	}
	for _, field := range providerSignals.Fields {
		item.Provenance = append(item.Provenance, Provenance{Signal: providerSignal(field), Source: "provider_adapter", Field: field, Experimental: true})
	}

	if raw := attrs[attrCompacted]; raw != "" {
		value, err := strconv.ParseBool(raw)
		switch {
		case err != nil:
			warnings = append(warnings, Warning{Code: "invalid_compaction_flag", SpanID: span.SpanID, Message: "gen_ai.conversation.compacted is not a boolean"})
		case value:
			item.Compaction.State = "detected"
		default:
			warnings = append(warnings, Warning{Code: "nonconformant_false_compaction", SpanID: span.SpanID, Message: "standard guidance says to leave compacted unset rather than record false"})
		}
	}
	if providerSignals.Compacted {
		item.Compaction.State = "detected"
	}
	item.Compaction.Armed = providerSignals.CompactionArmed

	item.Tokens.Input, warnings = parseToken(attrs, attrInputTokens, span.SpanID, warnings)
	item.Tokens.Output, warnings = parseToken(attrs, attrOutputTokens, span.SpanID, warnings)
	item.Tokens.InputShift = unknownDelta()
	item.Tokens.OutputShift = unknownDelta()

	for _, candidate := range []struct{ key, kind string }{{attrAnswerLabel, "label"}, {attrAnswerHash, "hash"}, {attrAnswerFingerprint, "fingerprint"}} {
		if value := attrs[candidate.key]; value != "" {
			item.fingerprint, item.fpKind = value, candidate.kind
			item.Provenance = append(item.Provenance, Provenance{Signal: "answer_drift", Source: "synthetic_attribute", Field: candidate.key, Experimental: true})
			break
		}
	}
	if span.DroppedAttributesCount > 0 {
		warnings = append(warnings, Warning{Code: "truncated_attributes", SpanID: span.SpanID, Message: fmt.Sprintf("source span dropped %d attributes; absent signals remain UNKNOWN", span.DroppedAttributesCount)})
	}
	if span.DroppedLinksCount > 0 {
		warnings = append(warnings, Warning{Code: "truncated_links", SpanID: span.SpanID, Message: fmt.Sprintf("source span dropped %d links; ancestry may be incomplete", span.DroppedLinksCount)})
	}
	for _, link := range span.Links {
		if link.DroppedAttributesCount > 0 {
			warnings = append(warnings, Warning{Code: "truncated_link_attributes", SpanID: span.SpanID, Message: fmt.Sprintf("a source span link dropped %d attributes; link provenance may be incomplete", link.DroppedAttributesCount)})
		}
	}
	return item, warnings, true
}

func firstAttributes(attrs []model.Attribute) map[string]string {
	out := make(map[string]string)
	for _, attr := range attrs {
		if _, exists := out[attr.Key]; !exists {
			out[attr.Key] = attr.Value
		}
	}
	return out
}

func addStandardProvenance(item *workingObservation, attrs map[string]string) {
	for _, entry := range []struct{ key, signal string }{
		{attrOperation, "operation"}, {attrProvider, "provider"}, {attrConversation, "conversation"},
		{attrCompacted, "compaction"}, {attrPreviousResponse, "previous_response"},
		{attrResponseID, "response"}, {attrInputTokens, "input_tokens"}, {attrOutputTokens, "output_tokens"},
	} {
		if attrs[entry.key] != "" {
			item.Provenance = append(item.Provenance, Provenance{Signal: entry.signal, Source: "otel_semantic_attribute", Field: entry.key, Experimental: true})
		}
	}
}

func providerSignal(field string) string {
	switch field {
	case openai.AttrResponseID:
		return "response"
	case openai.AttrPreviousResponse:
		return "previous_response"
	case openai.AttrOutputItemType:
		return "compaction"
	default:
		return "compaction_armed"
	}
}

func parseToken(attrs map[string]string, key, spanID string, warnings []Warning) (OptionalInt, []Warning) {
	raw := attrs[key]
	if raw == "" {
		return unknownInt(), warnings
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		warnings = append(warnings, Warning{Code: "invalid_token_count", SpanID: spanID, Message: key + " must be a non-negative integer"})
		return unknownInt(), warnings
	}
	return knownInt(value), warnings
}

func orderObservations(in []workingObservation) ([]workingObservation, []Warning) {
	byResponse := make(map[string]int)
	for i := range in {
		if in[i].Chain.ResponseID != "" {
			if _, exists := byResponse[in[i].Chain.ResponseID]; !exists {
				byResponse[in[i].Chain.ResponseID] = i
			}
		}
	}
	indegree := make([]int, len(in))
	children := make(map[int][]int)
	for i := range in {
		if p, ok := byResponse[in[i].Chain.PreviousResponseID]; ok && p != i {
			indegree[i]++
			children[p] = append(children[p], i)
		}
	}
	less := func(i, j int) bool {
		if in[i].StartedNs != in[j].StartedNs {
			return in[i].StartedNs < in[j].StartedNs
		}
		return in[i].SpanID < in[j].SpanID
	}
	var ready []int
	for i := range in {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
	out := make([]workingObservation, 0, len(in))
	seen := make([]bool, len(in))
	for len(ready) > 0 {
		index := ready[0]
		ready = ready[1:]
		seen[index] = true
		out = append(out, in[index])
		for _, child := range children[index] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
			}
		}
	}
	var warnings []Warning
	if len(out) != len(in) {
		var remaining []int
		for i := range in {
			if !seen[i] {
				remaining = append(remaining, i)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return less(remaining[i], remaining[j]) })
		for _, index := range remaining {
			out = append(out, in[index])
			warnings = append(warnings, Warning{Code: "response_chain_cycle", SpanID: in[index].SpanID, Message: "response ancestry is cyclic; time and span id used as fallback order"})
		}
	}
	return out, warnings
}

func resolveContinuity(traceID string, item *workingObservation, all []workingObservation, byResponse map[string]int, children map[string]int, warnings *[]Warning) {
	var linkedTargets []workingObservation
	var externalLinks []model.SpanLink
	for _, link := range item.span.Links {
		if link.TraceID != traceID {
			externalLinks = append(externalLinks, link)
			continue
		}
		for _, candidate := range all {
			if candidate.SpanID == link.SpanID {
				linkedTargets = append(linkedTargets, candidate)
				break
			}
		}
	}
	if len(linkedTargets) == 1 {
		item.Chain.LinkedSpanID = linkedTargets[0].SpanID
		item.Provenance = append(item.Provenance, Provenance{Signal: "response_link", Source: "otel_span_link", Field: linkedTargets[0].SpanID})
		if item.Chain.PreviousResponseID == "" && linkedTargets[0].Chain.ResponseID != "" {
			item.Chain.PreviousResponseID = linkedTargets[0].Chain.ResponseID
		}
	} else if len(linkedTargets) > 1 {
		item.Chain.Status = "ambiguous"
		*warnings = append(*warnings, Warning{Code: "ambiguous_span_links", SpanID: item.SpanID, Message: "multiple same-trace response links could supply ancestry"})
		return
	} else if len(externalLinks) == 1 {
		link := externalLinks[0]
		item.Chain.LinkedSpanID = link.SpanID
		item.Provenance = append(item.Provenance, Provenance{Signal: "response_link", Source: "otel_span_link", Field: link.TraceID + "/" + link.SpanID})
		if item.Chain.PreviousResponseID == "" {
			for _, attr := range link.Attributes {
				if attr.Key == attrResponseID && attr.Value != "" {
					item.Chain.PreviousResponseID = attr.Value
					break
				}
			}
		}
	} else if len(externalLinks) > 1 {
		item.Chain.Status = "ambiguous"
		*warnings = append(*warnings, Warning{Code: "ambiguous_span_links", SpanID: item.SpanID, Message: "multiple cross-trace links could supply response ancestry"})
		return
	}

	prev := item.Chain.PreviousResponseID
	switch {
	case prev == "" && item.span.DroppedLinksCount > 0:
		item.Chain.Status = "unknown"
	case prev == "":
		item.Chain.Status = "root"
	case len(externalLinks) == 1:
		item.Chain.Status = "linked_external"
	case children[prev] > 1:
		item.Chain.Status = "branched"
	case !hasResponse(byResponse, prev):
		item.Chain.Status = "missing_ancestry"
		*warnings = append(*warnings, Warning{Code: "missing_previous_response", SpanID: item.SpanID, Message: "previous response id has no matching span in this trace"})
	default:
		item.Chain.Status = "linked"
	}
	if item.Chain.LinkedSpanID != "" && prev != "" {
		for _, target := range linkedTargets {
			if target.Chain.ResponseID != "" && target.Chain.ResponseID != prev {
				item.Chain.Status = "conflict"
				*warnings = append(*warnings, Warning{Code: "link_attribute_conflict", SpanID: item.SpanID, Message: "span link target disagrees with previous response id"})
			}
		}
	}
}

func hasResponse(index map[string]int, responseID string) bool {
	_, ok := index[responseID]
	return ok
}

func contextReset(item workingObservation, previous *workingObservation) Indicator {
	if item.Compaction.State != "detected" {
		return Indicator{State: "unknown", Reason: "no positively confirmed compaction boundary"}
	}
	if previous == nil || item.Tokens.Input.State != "known" || previous.Tokens.Input.State != "known" {
		return Indicator{State: "unknown", Reason: "input token counts on both sides are required"}
	}
	if *item.Tokens.Input.Value < *previous.Tokens.Input.Value {
		return Indicator{State: "detected", Reason: "input token count decreased across a confirmed compaction boundary; structural signal only"}
	}
	return Indicator{State: "not_detected", Reason: "input token count did not decrease across the confirmed boundary"}
}

func drift(item workingObservation, previous *workingObservation) DriftIndicator {
	note := "compares supplied structural values only; does not measure semantic quality"
	if previous == nil || item.fingerprint == "" || previous.fingerprint == "" || item.fpKind != previous.fpKind {
		return DriftIndicator{State: "unknown", Note: note}
	}
	state := "unchanged"
	if item.fingerprint != previous.fingerprint {
		state = "changed"
	}
	return DriftIndicator{State: state, Kind: item.fpKind, Note: note}
}

func previousToken(previous *workingObservation, input bool) OptionalInt {
	if previous == nil {
		return unknownInt()
	}
	if input {
		return previous.Tokens.Input
	}
	return previous.Tokens.Output
}

func tokenDelta(before, after OptionalInt) TokenDelta {
	result := TokenDelta{Before: before, After: after, Delta: unknownInt()}
	if before.State == "known" && after.State == "known" {
		result.Delta = knownInt(*after.Value - *before.Value)
	}
	return result
}

func unknownDelta() TokenDelta {
	return TokenDelta{Before: unknownInt(), After: unknownInt(), Delta: unknownInt()}
}

func knownInt(value int64) OptionalInt { return OptionalInt{State: "known", Value: &value} }
func unknownInt() OptionalInt          { return OptionalInt{State: "unknown"} }

// WriteJSON emits the versioned report with stable indentation.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode compaction report: %w", err)
	}
	return nil
}

// WriteText renders a compact response-chain timeline.
func WriteText(w io.Writer, report Report) error {
	var out strings.Builder
	fmt.Fprintf(&out, "Compaction X-Ray v1  trace %s  %d source spans\n", report.TraceID, report.SourceSpanCount)
	if len(report.Observations) == 0 {
		out.WriteString("(no recognized GenAI response operations)\n")
	}
	for _, observation := range report.Observations {
		marker := "[?]"
		if observation.Compaction.State == "detected" {
			marker = "[COMPACT]"
		} else if observation.Compaction.Armed {
			marker = "[armed]"
		}
		response := observation.Chain.ResponseID
		if response == "" {
			response = "response=UNKNOWN"
		}
		ancestry := observation.Chain.Status
		if observation.Chain.PreviousResponseID != "" {
			ancestry += "←" + observation.Chain.PreviousResponseID
		}
		fmt.Fprintf(&out, "%02d %s %-20s %-24s in=%s out=%s Δin=%s reset=%s drift=%s\n",
			observation.Sequence, marker, response, ancestry,
			formatValue(observation.Tokens.Input), formatValue(observation.Tokens.Output),
			formatDelta(observation.Tokens.InputShift.Delta), observation.ContextReset.State,
			observation.AnswerDrift.State)
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&out, "warnings: %d\n", len(report.Warnings))
		for _, warning := range report.Warnings {
			location := warning.SpanID
			if location == "" {
				location = "trace"
			}
			fmt.Fprintf(&out, "  - %s %s: %s\n", location, warning.Code, warning.Message)
		}
	}
	out.WriteString("claim ceiling: structural indicators only; no semantic-quality claim\n")
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("write compaction report: %w", err)
	}
	return nil
}

func formatValue(value OptionalInt) string {
	if value.State != "known" || value.Value == nil {
		return "UNKNOWN"
	}
	return strconv.FormatInt(*value.Value, 10)
}

func formatDelta(value OptionalInt) string {
	if value.State != "known" || value.Value == nil {
		return "UNKNOWN"
	}
	if *value.Value > 0 {
		return "+" + strconv.FormatInt(*value.Value, 10)
	}
	return strconv.FormatInt(*value.Value, 10)
}
