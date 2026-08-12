// Package ledger normalizes token, cache, reasoning, tool, and explicitly
// supplied context-window signals from stored OpenTelemetry span attributes.
// It is a read-only projection over model.Trace: no provider or network calls.
package ledger

// ReportSchema is the versioned machine-readable cache/context ledger contract.
const ReportSchema = "grotto.cache_context_ledger.v1"

// RateSchema is the only accepted user-supplied rate-file contract.
const RateSchema = "grotto.token_rates.v1"

// SemconvSnapshot records the exact upstream semantics used by this version.
type SemconvSnapshot struct {
	Status   string `json:"status"`
	Core     string `json:"core"`
	GenAIRef string `json:"gen_ai_ref"`
}

// Source identifies one exact raw span attribute used or rejected by
// normalization. Derived explains provider-specific arithmetic, when needed.
type Source struct {
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	ValueType string `json:"value_type"`
	Family    string `json:"family"`
	Derived   string `json:"derived,omitempty"`
}

// Count is a knowledge-bearing token count. Value is null unless Status is
// known; a present zero is represented as known with value 0.
type Count struct {
	Status  string   `json:"status"`
	Value   *int64   `json:"value"`
	Reason  string   `json:"reason,omitempty"`
	Sources []Source `json:"sources,omitempty"`
}

// Usage is one normalized set of token signals. Cache, reasoning, and tool
// counts are explanatory subsets and are never added to Total.
type Usage struct {
	Input      Count `json:"input"`
	Output     Count `json:"output"`
	CacheRead  Count `json:"cache_read"`
	CacheWrite Count `json:"cache_write"`
	Reasoning  Count `json:"reasoning"`
	ToolInput  Count `json:"tool_input"`
	Total      Count `json:"total"`
}

// RetryAttribution groups attempts without erasing the cost of failed retries.
type RetryAttribution struct {
	LogicalCallID string `json:"logical_call_id,omitempty"`
	Attempt       *int64 `json:"attempt,omitempty"`
}

// Row is the causal per-span ledger record. Observed preserves normalized raw
// values; Contribution applies delta/cumulative/rollup aggregation semantics.
type Row struct {
	SpanID       string           `json:"span_id"`
	ParentSpanID string           `json:"parent_span_id,omitempty"`
	Name         string           `json:"name"`
	StartedNs    int64            `json:"started_ns"`
	EndedNs      int64            `json:"ended_ns"`
	Depth        int              `json:"depth"`
	BranchSpanID string           `json:"branch_span_id,omitempty"`
	Provider     string           `json:"provider,omitempty"`
	Model        string           `json:"model,omitempty"`
	Operation    string           `json:"operation,omitempty"`
	Mode         string           `json:"mode"`
	Series       string           `json:"series,omitempty"`
	ToolCall     bool             `json:"tool_call"`
	Retry        RetryAttribution `json:"retry"`
	Observed     Usage            `json:"observed"`
	Contribution Usage            `json:"contribution"`
	Contributes  bool             `json:"contributes"`
}

// ContextPressure is present only from an explicit positive context limit.
// Ratio is null if any required primary total is unknown.
type ContextPressure struct {
	Status string   `json:"status"`
	Limit  Count    `json:"limit"`
	Used   Count    `json:"used"`
	Ratio  *float64 `json:"ratio"`
	Reason string   `json:"reason,omitempty"`
}

// Issue is a deterministic reconciliation diagnostic.
type Issue struct {
	Code      string `json:"code"`
	SpanID    string `json:"span_id,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	Message   string `json:"message"`
}

// Summary is the trace rollup. Branches remain separately attributable in Rows.
type Summary struct {
	SpanCount     int             `json:"span_count"`
	UsageRows     int             `json:"usage_rows"`
	ToolCalls     int             `json:"tool_calls"`
	RetryAttempts int             `json:"retry_attempts"`
	Usage         Usage           `json:"usage"`
	Context       ContextPressure `json:"context"`
	Estimate      *Estimate       `json:"estimate,omitempty"`
}

// RateProvenance binds an estimate to the exact user-supplied local file.
type RateProvenance struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Schema   string `json:"schema"`
	AsOf     string `json:"as_of"`
	Currency string `json:"currency"`
}

// Estimate is omitted unless a rate file is explicitly supplied.
type Estimate struct {
	Status     string         `json:"status"`
	Amount     *string        `json:"amount"`
	Reason     string         `json:"reason,omitempty"`
	Provenance RateProvenance `json:"provenance"`
}

// Report is the versioned deterministic ledger projection for one trace.
type Report struct {
	Schema   string          `json:"schema"`
	Semconv  SemconvSnapshot `json:"semconv"`
	TraceID  string          `json:"trace_id"`
	RunLabel string          `json:"run_label"`
	Source   string          `json:"source"`
	Summary  Summary         `json:"summary"`
	Rows     []Row           `json:"rows"`
	Issues   []Issue         `json:"issues"`
}
