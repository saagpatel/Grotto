package redaction

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/saagpatel/grotto/internal/model"
)

const (
	redactionMask   = "‹redacted›"
	retainedPreview = "<retained>"
	maxPreviewBytes = 160
)

var quotedPathKeyRE = regexp.MustCompile(`\["(?:\\.|[^"\\])*"\]`)

// Evaluate returns a transformed copy and the exact raw-content-off plan used
// to create it. The input trace and its slices are never modified.
func (e *Evaluator) Evaluate(t model.Trace, opts Options) (Result, error) {
	out := t
	var decisions []Decision

	out.RunLabel, decisions = e.evaluateStructural("trace.run_label", "str", t.RunLabel, decisions)
	out.RootName, decisions = e.evaluateStructural("trace.root_name", "str", t.RootName, decisions)
	out.Spans = make([]model.Span, len(t.Spans))
	for i, originalSpan := range t.Spans {
		sp := originalSpan
		spanPath := fmt.Sprintf("spans[%d]", i)
		sp.Name, decisions = e.evaluateStructural(spanPath+".name", "str", originalSpan.Name, decisions)
		sp.Attributes = make([]model.Attribute, 0, len(originalSpan.Attributes))
		for _, originalAttr := range originalSpan.Attributes {
			rawPath := spanPath + ".attributes[" + strconv.Quote(originalAttr.Key) + "]"
			keyRule := e.winner("metadata.attribute_key", originalAttr.Key)
			if keyRule != nil && keyRule.Action != ActionRetain {
				decisions = append(decisions, e.sensitiveKeyDecision(rawPath, originalAttr.Key, *keyRule))
				continue
			}
			attr, attrDecisions, keep, err := e.evaluateAttribute(rawPath, originalAttr)
			if err != nil {
				return Result{}, err
			}
			decisions = append(decisions, attrDecisions...)
			if keep {
				sp.Attributes = append(sp.Attributes, attr)
			}
		}
		sp.Links = make([]model.SpanLink, len(originalSpan.Links))
		for linkIndex, originalLink := range originalSpan.Links {
			link := originalLink
			linkPath := fmt.Sprintf("%s.links[%d]", spanPath, linkIndex)
			if originalLink.TraceState != "" {
				link.TraceState, decisions = e.evaluateStructural(linkPath+".trace_state", "str", originalLink.TraceState, decisions)
			}
			link.Attributes = make([]model.Attribute, 0, len(originalLink.Attributes))
			for _, originalAttr := range originalLink.Attributes {
				rawPath := linkPath + ".attributes[" + strconv.Quote(originalAttr.Key) + "]"
				keyRule := e.winner("metadata.attribute_key", originalAttr.Key)
				if keyRule != nil && keyRule.Action != ActionRetain {
					decisions = append(decisions, e.sensitiveKeyDecision(rawPath, originalAttr.Key, *keyRule))
					continue
				}
				attr, attrDecisions, keep, err := e.evaluateAttribute(rawPath, originalAttr)
				if err != nil {
					return Result{}, err
				}
				decisions = append(decisions, attrDecisions...)
				if keep {
					link.Attributes = append(link.Attributes, attr)
				}
			}
			sp.Links[linkIndex] = link
		}
		out.Spans[i] = sp
	}

	sort.SliceStable(decisions, func(i, j int) bool {
		if decisions[i].Path != decisions[j].Path {
			return decisions[i].Path < decisions[j].Path
		}
		return decisions[i].MatchedRule < decisions[j].MatchedRule
	})
	report := Report{
		Schema: ReportSchemaV1,
		Policy: PolicyProvenance{
			PolicyID: e.policy.PolicyID, PolicyVersion: e.policy.Version,
			PolicySHA256: e.digest, EvaluatorVersion: EvaluatorVersion,
		},
		Source: ReportSource{
			Kind:    e.safeMetadata(opts.SourceKind),
			Ref:     e.safeMetadata(opts.SourceRef),
			TraceID: e.safeMetadata(t.TraceID),
		},
		Decisions: decisions,
	}
	report.Summary = summarize(decisions)
	return Result{Trace: out, Report: report}, nil
}

func (e *Evaluator) evaluateStructural(path, valueType, value string, decisions []Decision) (string, []Decision) {
	transformed, decision, keep := e.applyScalar(path, valueType, value)
	if !keep {
		transformed = ""
	}
	return transformed, append(decisions, decision)
}

func (e *Evaluator) evaluateAttribute(path string, attr model.Attribute) (model.Attribute, []Decision, bool, error) {
	if !knownValueType(attr.ValueType) {
		decision := e.unknownDecision(path, "unknown", len(attr.Value),
			"implicit.unknown-value-type", "unknown_value_type", "Unsupported attribute value_type is dropped fail-closed.")
		return attr, []Decision{decision}, false, nil
	}
	if e.shouldInspectJSON(path, attr.ValueType) {
		// Global value-pattern masks must not shadow a structural field action.
		// For example, a credential inside gen_ai.input.messages is masked as a
		// nested value, but the parent field still has to be dropped in full.
		if rule := e.winnerJSONFieldAction(path, attr.Value); rule != nil && rule.Action != ActionRetain {
			transformed, decision, keep := e.applyRule(path, attr.ValueType, attr.Value, rule)
			attr.Value = transformed
			if rule.Action != ActionRetain {
				attr.ValueType = "str"
			}
			return attr, []Decision{decision}, keep, nil
		}
		value, err := decodeJSONValue(attr.Value)
		if err != nil {
			decision := e.unknownDecision(path, attr.ValueType, len(attr.Value),
				"implicit.malformed-json", "malformed_json", "Declared JSON could not be parsed; the attribute is dropped fail-closed.")
			return attr, []Decision{decision}, false, nil
		}
		transformed, keep, decisions := e.walkJSON(path+".json", value, 0)
		if !keep {
			return attr, decisions, false, nil
		}
		encoded, err := json.Marshal(transformed)
		if err != nil {
			return attr, nil, false, fmt.Errorf("encode sanitized JSON at %s: %w", path, err)
		}
		if len(encoded) > e.policy.MaxValueBytes {
			decisions = append(decisions, e.unknownDecision(path, attr.ValueType, len(attr.Value),
				"implicit.json-size-limit", "sanitized_json_too_large", "Sanitized JSON exceeds the policy value bound; the attribute is dropped to preserve valid JSON."))
			return attr, decisions, false, nil
		}
		attr.Value = string(encoded)
		attr.ValueType = "json"
		if len(decisions) == 0 {
			_, decision, _ := e.applyScalar(path, attr.ValueType, attr.Value)
			decisions = append(decisions, decision)
		}
		return attr, decisions, true, nil
	}

	transformed, decision, keep := e.applyScalar(path, attr.ValueType, attr.Value)
	attr.Value = transformed
	if decision.Action != ActionRetain {
		attr.ValueType = "str"
	}
	return attr, []Decision{decision}, keep, nil
}

func knownValueType(valueType string) bool {
	switch valueType {
	case "str", "int", "float", "bool", "bytes", "json":
		return true
	default:
		return false
	}
}

func (e *Evaluator) walkJSON(path string, value any, depth int) (any, bool, []Decision) {
	if depth > e.policy.MaxDepth {
		decision := e.unknownDecision(path, jsonType(value), jsonValueLength(value),
			"implicit.depth-limit", "nested_depth_exceeded", "Nested JSON exceeds max_depth; the subtree is dropped fail-closed.")
		return nil, false, []Decision{decision}
	}
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(v))
		var decisions []Decision
		for _, key := range keys {
			childPath := path + "[" + strconv.Quote(key) + "]"
			if rule := e.winner("metadata.json_key", key); rule != nil && rule.Action != ActionRetain {
				decisions = append(decisions, e.sensitiveKeyDecision(childPath, key, *rule))
				continue
			}
			child, keep, childDecisions := e.walkJSON(childPath, v[key], depth+1)
			decisions = append(decisions, childDecisions...)
			if keep {
				out[key] = child
			}
		}
		return out, true, decisions
	case []any:
		out := make([]any, len(v))
		var decisions []Decision
		for i, item := range v {
			child, keep, childDecisions := e.walkJSON(fmt.Sprintf("%s[%d]", path, i), item, depth+1)
			decisions = append(decisions, childDecisions...)
			if keep {
				out[i] = child
			}
		}
		return out, true, decisions
	case string:
		transformed, decision, keep := e.applyScalar(path, "json-string", v)
		return transformed, keep, []Decision{decision}
	case json.Number:
		transformed, decision, keep := e.applyScalar(path, "json-number", v.String())
		if decision.Action == ActionRetain {
			return v, keep, []Decision{decision}
		}
		return transformed, keep, []Decision{decision}
	case bool:
		transformed, decision, keep := e.applyScalar(path, "json-bool", strconv.FormatBool(v))
		if decision.Action == ActionRetain {
			return v, keep, []Decision{decision}
		}
		return transformed, keep, []Decision{decision}
	case nil:
		_, decision, keep := e.applyScalar(path, "json-null", "null")
		return nil, keep, []Decision{decision}
	default:
		decision := e.unknownDecision(path, "json-unknown", 0,
			"implicit.unknown-json-type", "unknown_json_type", "Unsupported nested JSON type is dropped fail-closed.")
		return nil, false, []Decision{decision}
	}
}

func (e *Evaluator) applyScalar(path, valueType, value string) (string, Decision, bool) {
	rule := e.winner(path, value)
	return e.applyRule(path, valueType, value, rule)
}

func (e *Evaluator) applyRule(path, valueType, value string, rule *compiledRule) (string, Decision, bool) {
	if rule == nil {
		rule = implicitRule("implicit.retain", ActionRetain, "custom", "No policy rule matched; the applied copy retains the field while preview hides its raw value.", 0)
	}
	if rule.Action == ActionRetain && valueType == "bytes" {
		rule = implicitRule("implicit.binary-digest", ActionHash, "binary", "Binary values use a stable digest; raw bytes are not rendered.", 0)
	}
	if rule.Action == ActionRetain && len(value) > e.policy.MaxValueBytes {
		rule = implicitRule("implicit.value-size-limit", ActionTruncate, "oversized", "Value exceeds max_value_bytes and is truncated after policy matching.", e.policy.MaxValueBytes)
	}

	decision := Decision{
		Path: safeDecisionPath(path), Category: rule.Category, MatchedRule: rule.ID,
		RuleProvenance: rule.Provenance, Action: rule.Action,
		Explanation: rule.Explanation,
		Precedence: fmt.Sprintf("priority=%d;literal=%d;wildcards=%d;rule_id=%s",
			rule.Priority, rule.literalSize, rule.wildcards, rule.ID),
		OriginalType: valueType, OriginalLengthBytes: len(value), Status: "known",
	}

	switch rule.Action {
	case ActionRetain:
		decision.Preview = retainedPreview
		return value, decision, true
	case ActionMask:
		replacement := rule.Replacement
		if replacement == "" {
			replacement = redactionMask
		}
		masked := replacement
		if rule.valueRE != nil {
			masked = rule.valueRE.ReplaceAllStringFunc(value, func(string) string { return replacement })
		}
		masked = e.maskRemainingCandidates(path, masked, rule.ID)
		masked = truncateUTF8(masked, e.policy.MaxValueBytes)
		decision.Preview = previewValue(masked)
		return masked, decision, true
	case ActionHash:
		digest := value
		if !strings.HasPrefix(value, "sha256:v1:") {
			digest = stableDigest(value)
		}
		decision.Digest = digest
		return digest, decision, true
	case ActionTruncate:
		limit := rule.MaxLength
		if limit <= 0 || limit > e.policy.MaxValueBytes {
			limit = e.policy.MaxValueBytes
		}
		truncated := truncateUTF8(e.maskRemainingCandidates(path, value, rule.ID), limit)
		decision.Preview = fmt.Sprintf("<truncated:%dB->%dB>", len(value), len(truncated))
		return truncated, decision, true
	case ActionDrop:
		decision.Preview = "<dropped>"
		return "", decision, false
	default:
		decision.Status = "unknown"
		decision.UnknownReason = "unsupported_action"
		decision.Action = ActionDrop
		decision.Preview = "<dropped:unknown>"
		return "", decision, false
	}
}

func (e *Evaluator) winnerJSONFieldAction(path, value string) *compiledRule {
	for i := range e.rules {
		rule := &e.rules[i]
		if !rule.pathRE.MatchString(path) {
			continue
		}
		if rule.valueRE != nil && !rule.valueRE.MatchString(value) {
			continue
		}
		if rule.Path == "*" && rule.valueRE != nil {
			continue
		}
		return rule
	}
	return nil
}

func (e *Evaluator) maskRemainingCandidates(path, value, winningRuleID string) string {
	masked := value
	for i := range e.rules {
		rule := &e.rules[i]
		if rule.ID == winningRuleID || rule.Action != ActionMask || rule.valueRE == nil ||
			!rule.pathRE.MatchString(path) || !rule.valueRE.MatchString(masked) {
			continue
		}
		replacement := rule.Replacement
		if replacement == "" {
			replacement = redactionMask
		}
		masked = rule.valueRE.ReplaceAllStringFunc(masked, func(string) string { return replacement })
	}
	return masked
}

func (e *Evaluator) winner(path, value string) *compiledRule {
	for i := range e.rules {
		rule := &e.rules[i]
		if !rule.pathRE.MatchString(path) {
			continue
		}
		if rule.valueRE != nil && !rule.valueRE.MatchString(value) {
			continue
		}
		return rule
	}
	return nil
}

func (e *Evaluator) shouldInspectJSON(path, valueType string) bool {
	if valueType == "json" {
		return true
	}
	for _, re := range e.jsonPathREs {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

func (e *Evaluator) sensitiveKeyDecision(_ string, key string, rule compiledRule) Decision {
	digest := stableDigest(key)
	safePath := "<redacted-field:" + digest[len("sha256:v1:"):len("sha256:v1:")+12] + ">"
	return Decision{
		Path: safePath, Category: rule.Category, MatchedRule: rule.ID,
		RuleProvenance: rule.Provenance, Action: ActionDrop,
		Explanation:  "The field key itself matched a sensitive-value rule and is dropped; its raw path is not reportable.",
		Precedence:   fmt.Sprintf("priority=%d;literal=%d;wildcards=%d;rule_id=%s", rule.Priority, rule.literalSize, rule.wildcards, rule.ID),
		OriginalType: "field-key", OriginalLengthBytes: len(key), Preview: "<dropped>", Status: "known",
	}
}

func (e *Evaluator) unknownDecision(path, valueType string, length int, ruleID, reason, explanation string) Decision {
	return Decision{
		Path: safeDecisionPath(path), Category: "unknown", MatchedRule: ruleID,
		RuleProvenance: "evaluator.fail-closed", Action: ActionDrop,
		Explanation: explanation, Precedence: "implicit fail-closed safety bound",
		OriginalType: valueType, OriginalLengthBytes: length,
		Preview: "<dropped:unknown>", Status: "unknown", UnknownReason: reason,
	}
}

func safeDecisionPath(path string) string {
	return quotedPathKeyRE.ReplaceAllStringFunc(path, func(segment string) string {
		quotedKey := segment[1 : len(segment)-1]
		key, err := strconv.Unquote(quotedKey)
		if err != nil {
			key = segment
		}
		digest := stableDigest(key)
		shortDigest := digest[len("sha256:v1:") : len("sha256:v1:")+12]
		return `["<field:` + shortDigest + `>"]`
	})
}

func (e *Evaluator) safeMetadata(value string) string {
	if value == "" {
		return "unknown"
	}
	if rule := e.winner("metadata.report", value); rule != nil && rule.Action != ActionRetain {
		digest := stableDigest(value)
		return "<redacted-ref:" + digest[len("sha256:v1:"):len("sha256:v1:")+12] + ">"
	}
	return truncateUTF8(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value), 128)
}

func implicitRule(id string, action Action, category, explanation string, maxLength int) *compiledRule {
	return &compiledRule{Rule: Rule{
		ID: id, Priority: -1, Path: "*", Category: category, Action: action,
		Explanation: explanation, Provenance: "evaluator.v1", MaxLength: maxLength,
	}, wildcards: 1}
}

func stableDigest(value string) string {
	sum := sha256.Sum256(append([]byte("grotto:redaction:v1\x00"), []byte(value)...))
	return "sha256:v1:" + hex.EncodeToString(sum[:])
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes < 1 || len(value) <= maxBytes {
		return value
	}
	const marker = "…"
	if maxBytes <= len(marker) {
		return strings.Repeat(".", maxBytes)
	}
	cut := maxBytes - len(marker)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + marker
}

func previewValue(value string) string {
	return truncateUTF8(value, maxPreviewBytes)
}

func decodeJSONValue(value string) (any, error) {
	dec := json.NewDecoder(bytes.NewBufferString(value))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		return nil, err
	}
	return nil, fmt.Errorf("multiple JSON values")
}

func jsonType(value any) string {
	switch value.(type) {
	case map[string]any:
		return "json-object"
	case []any:
		return "json-array"
	case string:
		return "json-string"
	case json.Number:
		return "json-number"
	case bool:
		return "json-bool"
	case nil:
		return "json-null"
	default:
		return "json-unknown"
	}
}

func jsonValueLength(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func summarize(decisions []Decision) Summary {
	summary := Summary{Fields: len(decisions)}
	for _, decision := range decisions {
		switch decision.Action {
		case ActionRetain:
			summary.Retained++
		case ActionMask:
			summary.Masked++
		case ActionHash:
			summary.Hashed++
		case ActionTruncate:
			summary.Truncated++
		case ActionDrop:
			summary.Dropped++
		}
		if decision.Status == "unknown" {
			summary.Unknown++
		}
	}
	return summary
}
