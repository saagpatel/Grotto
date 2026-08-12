// Package redaction evaluates versioned disclosure policies against Grotto
// traces. The same evaluator powers dry-run previews and the store's ingest
// chokepoint so their behavior cannot drift.
package redaction

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// PolicySchemaV1 is the policy contract understood by this evaluator.
	PolicySchemaV1 = "grotto.redaction-policy.v1"
	// ReportSchemaV1 is the safe preview report contract emitted by this evaluator.
	ReportSchemaV1 = "grotto.redaction-preview.v1"
	// EvaluatorVersion identifies action semantics independently of policy data.
	EvaluatorVersion = "1.0.0"
)

// Action is the applied disclosure treatment selected for a field.
type Action string

// Policy V1 actions.
const (
	ActionRetain   Action = "retain"
	ActionMask     Action = "mask"
	ActionHash     Action = "hash"
	ActionTruncate Action = "truncate"
	ActionDrop     Action = "drop"
)

// Policy is the machine-readable redaction policy contract.
type Policy struct {
	Schema           string   `json:"schema"`
	PolicyID         string   `json:"policy_id"`
	Version          string   `json:"version"`
	Description      string   `json:"description"`
	MaxDepth         int      `json:"max_depth"`
	MaxValueBytes    int      `json:"max_value_bytes"`
	InspectJSONPaths []string `json:"inspect_json_paths"`
	Rules            []Rule   `json:"rules"`
}

// Rule selects one action for matching field paths and, optionally, values.
type Rule struct {
	ID          string `json:"id"`
	Priority    int    `json:"priority"`
	Path        string `json:"path"`
	ValueRegex  string `json:"value_regex,omitempty"`
	Category    string `json:"category"`
	Action      Action `json:"action"`
	Explanation string `json:"explanation"`
	Provenance  string `json:"provenance"`
	Replacement string `json:"replacement,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
}

type compiledRule struct {
	Rule
	pathRE      *regexp.Regexp
	valueRE     *regexp.Regexp
	literalSize int
	wildcards   int
}

// Evaluator is an immutable compiled policy.
type Evaluator struct {
	policy      Policy
	rules       []compiledRule
	jsonPathREs []*regexp.Regexp
	digest      string
}

//go:embed default_policy_v1.json
var defaultPolicyJSON []byte

var (
	defaultOnce      sync.Once
	defaultEvaluator *Evaluator
	defaultErr       error
)

// LoadPolicy decodes and compiles a Policy V1 document.
func LoadPolicy(r io.Reader) (*Evaluator, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var policy Policy
	if err := dec.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode redaction policy: %w", err)
	}
	if err := requireEOF(dec); err != nil {
		return nil, err
	}
	return NewEvaluator(policy)
}

// DefaultEvaluator returns the immutable, process-cached embedded safe default.
func DefaultEvaluator() (*Evaluator, error) {
	defaultOnce.Do(func() {
		defaultEvaluator, defaultErr = LoadPolicy(strings.NewReader(string(defaultPolicyJSON)))
	})
	return defaultEvaluator, defaultErr
}

// NewEvaluator validates and compiles a policy.
func NewEvaluator(policy Policy) (*Evaluator, error) {
	if policy.Schema != PolicySchemaV1 {
		return nil, fmt.Errorf("policy schema %q is unsupported", policy.Schema)
	}
	if !safeID(policy.PolicyID) || !safeID(policy.Version) {
		return nil, fmt.Errorf("policy_id and version must contain only letters, digits, dot, underscore, or dash")
	}
	if policy.MaxDepth < 1 || policy.MaxDepth > 64 {
		return nil, fmt.Errorf("max_depth must be between 1 and 64")
	}
	if policy.MaxValueBytes < 32 || policy.MaxValueBytes > 16*1024*1024 {
		return nil, fmt.Errorf("max_value_bytes must be between 32 and 16777216")
	}

	e := &Evaluator{policy: policy}
	seen := make(map[string]struct{}, len(policy.Rules))
	for _, rule := range policy.Rules {
		if !safeID(rule.ID) {
			return nil, fmt.Errorf("rule id %q is invalid", rule.ID)
		}
		if _, ok := seen[rule.ID]; ok {
			return nil, fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		if rule.Path == "" || rule.Category == "" || rule.Explanation == "" || rule.Provenance == "" {
			return nil, fmt.Errorf("rule %q requires path, category, explanation, and provenance", rule.ID)
		}
		switch rule.Action {
		case ActionRetain, ActionMask, ActionHash, ActionDrop:
		case ActionTruncate:
			if rule.MaxLength < 1 {
				return nil, fmt.Errorf("truncate rule %q requires max_length", rule.ID)
			}
		default:
			return nil, fmt.Errorf("rule %q has unsupported action %q", rule.ID, rule.Action)
		}
		pathRE, literal, wildcards, err := compileGlob(rule.Path)
		if err != nil {
			return nil, fmt.Errorf("rule %q path: %w", rule.ID, err)
		}
		var valueRE *regexp.Regexp
		if rule.ValueRegex != "" {
			valueRE, err = regexp.Compile(rule.ValueRegex)
			if err != nil {
				return nil, fmt.Errorf("rule %q value_regex: %w", rule.ID, err)
			}
		}
		e.rules = append(e.rules, compiledRule{
			Rule: rule, pathRE: pathRE, valueRE: valueRE,
			literalSize: literal, wildcards: wildcards,
		})
	}
	for _, pattern := range policy.InspectJSONPaths {
		re, _, _, err := compileGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("inspect_json_paths %q: %w", pattern, err)
		}
		e.jsonPathREs = append(e.jsonPathREs, re)
	}

	sort.Slice(e.rules, func(i, j int) bool {
		if e.rules[i].Priority != e.rules[j].Priority {
			return e.rules[i].Priority > e.rules[j].Priority
		}
		if e.rules[i].literalSize != e.rules[j].literalSize {
			return e.rules[i].literalSize > e.rules[j].literalSize
		}
		if e.rules[i].wildcards != e.rules[j].wildcards {
			return e.rules[i].wildcards < e.rules[j].wildcards
		}
		return e.rules[i].ID < e.rules[j].ID
	})
	canonical, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("canonicalize policy: %w", err)
	}
	sum := sha256.Sum256(canonical)
	e.digest = hex.EncodeToString(sum[:])
	return e, nil
}

func safeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			continue
		}
		return false
	}
	return true
}

func compileGlob(pattern string) (*regexp.Regexp, int, int, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	literal, wildcards := 0, 0
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
			wildcards++
		case '?':
			b.WriteByte('.')
			wildcards++
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
			literal++
		}
	}
	b.WriteByte('$')
	re, err := regexp.Compile(b.String())
	return re, literal, wildcards, err
}

func requireEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode redaction policy: multiple JSON values")
		}
		return fmt.Errorf("decode redaction policy trailer: %w", err)
	}
	return nil
}
