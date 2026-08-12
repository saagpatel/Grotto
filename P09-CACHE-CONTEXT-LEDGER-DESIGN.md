# P09 — Cache and Context Ledger

**Selected additive product increment:** P09
**Status:** implementation selected
**Owner:** `codex/p09-cache-context-ledger`
**As-of:** 2026-08-11
**Starting binding:** `origin/main@6d40420bd2c6c435847cd68758c3858df8053eaf`

P09 adds a deterministic causal usage ledger over Grotto's existing, genuine
OpenTelemetry spans. It does not add a provider client, pricing lookup, cloud
backend, alternate trace shape, or second store. The vertical slice remains:

```text
OTLP span attributes -> model.Trace from SQLite -> internal/ledger
  -> grotto show --ledger / --ledger-json -> deterministic report
```

## Ownership boundary

P09 owns:

- `internal/ledger/**` for normalization, reconciliation, aggregation, rates,
  schemas, rendering, fixtures, and focused tests;
- `schemas/cache-context-ledger-v1.schema.json` and
  `schemas/token-rates-v1.schema.json`;
- this design, the P09 fixture/demo, and P09 command documentation;
- the smallest required integration in `internal/cli/show.go` and its tests.

P06 owns compaction visualization. P08 owns redaction preview. P09 may read the
existing `gen_ai.conversation.compacted` signal and must verify that usage still
works after the existing ingest-time redaction pass, but it does not implement
either sibling surface.

## Current primary-source findings

The research baseline is the official OpenTelemetry repositories, read on
2026-08-11:

- OpenTelemetry Semantic Conventions **v1.44.0** (published 2026-08-04) moved
  GenAI conventions into the dedicated
  [`semantic-conventions-genai`](https://github.com/open-telemetry/semantic-conventions-genai)
  repository.
- The GenAI repository was read at
  [`8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958`](https://github.com/open-telemetry/semantic-conventions-genai/tree/8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958).
  It had no tagged release or schema URL. Its span, metric, attribute, OpenAI,
  and Anthropic documents all label the conventions **Development**. P09 must
  therefore preserve raw provenance and version its own report; it must not
  present these names as stable.
- The official [GenAI span convention](https://github.com/open-telemetry/semantic-conventions-genai/blob/8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958/docs/gen-ai/gen-ai-spans.md)
  recommends integer span attributes for input, output, cache-read,
  cache-creation, and reasoning token counts. Input includes cached input;
  reasoning is included in output. Cache and reasoning values are therefore
  explanatory subsets, never extra terms in `input + output`.
- The official [GenAI metric convention](https://github.com/open-telemetry/semantic-conventions-genai/blob/8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958/docs/gen-ai/gen-ai-metrics.md)
  defines `gen_ai.client.token.usage` as a Development histogram with UCUM unit
  `{token}`, split by `gen_ai.token.type=input|output`. P09 consumes spans, not
  metrics, so it does not synthesize metric samples or treat histograms as
  cumulative span counts.
- The official [OpenAI refinement](https://github.com/open-telemetry/semantic-conventions-genai/blob/8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958/docs/gen-ai/openai.md)
  maps total input to `usage.input_tokens`, cache read to
  `usage.input_tokens_details.cached_tokens`, and reasoning output to
  `usage.output_tokens_details.reasoning_tokens`.
- The official [Anthropic refinement](https://github.com/open-telemetry/semantic-conventions-genai/blob/8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958/docs/gen-ai/anthropic.md)
  states that Anthropic `input_tokens` excludes cached tokens and requires the
  normalized total to be `input_tokens + cache_read_input_tokens +
  cache_creation_input_tokens`.
- The standard defines `execute_tool` spans and tool identity, but no standard
  tool-token count or context-window limit attribute. P09 leaves those token
  values `UNKNOWN` unless an explicit Grotto extension is supplied. It never
  derives either from text length, model name, or a provider call.

These are **standard requirements** above. The normalization and extension
rules below are **Grotto design inferences**. Synthetic fixture values are
**local test behavior**, not evidence of live provider interoperability.

## Field mapping and precedence

All accepted values must be integer OTel span attributes. Presence is tracked
separately from value, so zero is known zero and absence is `UNKNOWN`.

| Normalized signal | Standard attribute | Narrow provider payload alias carried as a span attribute | Inclusion |
|---|---|---|---|
| input | `gen_ai.usage.input_tokens` | OpenAI `openai.usage.input_tokens`; Anthropic `anthropic.usage.input_tokens` | primary total |
| output | `gen_ai.usage.output_tokens` | `openai.usage.output_tokens`; `anthropic.usage.output_tokens` | primary total |
| cache read | `gen_ai.usage.cache_read.input_tokens` | `openai.usage.input_tokens_details.cached_tokens`; `anthropic.usage.cache_read_input_tokens` | subset of input |
| cache write | `gen_ai.usage.cache_creation.input_tokens` | `anthropic.usage.cache_creation_input_tokens` | subset of input |
| reasoning | `gen_ai.usage.reasoning.output_tokens` | `openai.usage.output_tokens_details.reasoning_tokens` | subset of output |
| tool input | none | `grotto.usage.tool.input_tokens` | annotated subset of input |
| context limit | none | `grotto.context.window.limit_tokens` | denominator only |
| count mode | per-operation is the standard span interpretation | `grotto.usage.mode=delta|cumulative|rollup` | aggregation control |
| cumulative series | none | `grotto.usage.series` | partitions cumulative samples |
| retry group | none | `grotto.retry.logical_call_id`, `grotto.retry.attempt` | attribution only; every attempt counts |

Deprecated OTel `gen_ai.usage.prompt_tokens` and
`gen_ai.usage.completion_tokens` are accepted at lower precedence and always
reported as deprecated provenance. Standard names outrank provider aliases,
which outrank deprecated names. Equal duplicates resolve once and produce a
diagnostic; conflicting candidates make that signal `UNKNOWN` instead of
choosing silently.

Provider aliases are deliberately narrow local ingestion aliases, not claims
that those keys are OTel semantic conventions. They are interpreted only when
`gen_ai.provider.name` matches. OpenAI cache and reasoning are already included
in its totals. Anthropic raw input is expanded with its cache-read and
cache-creation fields exactly once. Checked arithmetic rejects negative values
and overflow.

## Normalized model and aggregation

Each causal row retains trace/span/parent IDs, depth, branch, timing, provider,
model, operation, retry metadata, normalized signals, and every accepted or
rejected raw source attribute.

`grotto.usage.mode` controls contribution semantics:

- `delta` (default): the span reports its own operation. Add it once.
- `cumulative`: values are monotonically increasing samples within
  `grotto.usage.series`; sort by `(started_ns, ended_ns, span_id)` and contribute
  the checked delta from the previous sample. A decrease is impossible and
  becomes `UNKNOWN` with a reconciliation issue.
- `rollup`: the span reports a total over descendants. Display it, exclude it
  from additive totals, and reconcile it against contributing descendant rows.
  If no additive evidence exists for a signal, a single unconflicted rollup may
  supply the trace summary while per-span attribution remains explicitly
  rollup-sourced.

The trace total is `input + output`. Cache-read, cache-write, reasoning, and
tool-input are explanatory subsets and are never added again. Parallel branches
retain their top-level branch span ID and aggregate independently before the
trace rollup. Retries remain separate causal rows; the logical-call ID groups
them but never erases consumed tokens.

Rows are ordered by `(started_ns, ended_ns, span_id)` rather than ingest order.
Parent depth and branch are computed defensively from the flat stored spans;
orphans and cycles remain visible with diagnostics instead of disappearing.

## Reconciliation and UNKNOWN rules

The report emits stable issue codes for:

- conflicting equal-precedence or cross-family totals;
- equal duplicated usage observations;
- negative counts, arithmetic overflow, and impossible subset greater than
  its containing total;
- cumulative decreases or missing series identity;
- rollup-versus-descendant unexplained deltas;
- conflicting context limits;
- malformed retry attempts or rate files.

Context pressure is computed only when a positive limit is explicitly supplied
and primary input/output totals are known. The report retains the limit's raw
attribute provenance. No model-name lookup or guessed context size is allowed.

## Optional monetary estimates

Token accounting is the primary report. `--ledger-rates <file>` accepts only a
local `grotto.token-rates.v1` JSON document with an `as_of`, currency, exact
provider/model match, and user-supplied per-million-token rates. The report
retains the file path, SHA-256 digest, schema, as-of value, currency, and matched
rate entry. Runtime code contains no prices and makes no network calls.

Input cost is partitioned into uncached, cache-read, and cache-write input so
subsets are not double counted. Output is partitioned into visible/non-reasoning
and reasoning output. Missing specialized rates fall back only to the explicit
input or output rate in the same matched user entry. A missing match or unknown
component yields `UNKNOWN`, never zero.

## Product surface and report contracts

```text
grotto show <trace-id> --ledger
grotto show <trace-id> --ledger-json
grotto show <trace-id> --ledger --ledger-rates ./rates.json
```

`grotto.cache_context_ledger.v1` is the machine report schema. Text output is a
deterministic projection: concise trace summary, causal per-span rows, explicit
raw sources, and reconciliation issues. `--ledger-json` is intended for exact
source-attribute reconciliation and schema validation.

## Five-minute deterministic demo

1. Point `GROTTO_DB` at a temporary local database.
2. Ingest `internal/ledger/testdata/agent_trace.json` through the existing OTLP
   mapping/store test helper; it contains uncached, cache-hit/write, mixed
   provider, reasoning, tool, retry, parallel, compaction-adjacent, missing
   limit, conflict, zero, and out-of-order cases.
3. Run `grotto show <trace-id> --ledger` and inspect causal rows plus issues.
4. Run `grotto show <trace-id> --ledger-json`, validate the V1 schema, and trace
   each known total to its exact attribute key/value.
5. Add `--ledger-rates internal/ledger/testdata/rates.json` and confirm the
   estimate includes the supplied schema/as-of/digest provenance. Remove the
   flag and confirm no monetary fields are produced.

## Acceptance and claim ceiling

P09 is locally complete when the fixture produces the same report bytes across
runs; every known total reconciles to exact source attributes; malformed and
privacy cases fail closed; `gofmt`, `go test ./...`, `CGO_ENABLED=0 go build
./...`, and available `golangci-lint` pass; and the feature branch is delivered
through the authorized Git workflow. This proves deterministic local behavior,
not ecosystem adoption, live provider interoperability, runtime uptake, or
production safety.
