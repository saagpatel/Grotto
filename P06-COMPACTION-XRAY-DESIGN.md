# P06 Compaction X-Ray Design

Status: admitted additive phase; implementation authorized

As of: 2026-08-11

## Scope and ownership

P06 adds a local, deterministic Compaction X-Ray for OpenTelemetry GenAI traces.
It owns compaction and response-chain visualization only. P08 owns redaction
preview; P09 owns token and cache ledgers. P06 reads already-redacted span data
and shows only boundary-local token deltas, never a cross-trace usage ledger.

Owned implementation surfaces:

- `internal/compaction/**`: provider-neutral normalization, versioned report, and
  stable text rendering.
- `internal/compaction/openai/**`: optional experimental OpenAI Responses adapter.
- `tests/fixtures/compaction/**`: synthetic OTLP JSON only; no transcripts.
- Real OTel span-link and dropped-field preservation in the shared model/store,
  plus minimal `grotto compaction` CLI registration.
- This design, the additive roadmap entry, and concise command/demo documentation.

## Current primary sources

### Standards requirements

- OpenTelemetry GenAI semantic conventions, dedicated repository `main` at
  commit `8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958` (read 2026-08-11): GenAI
  inference spans are Development status. `gen_ai.operation.name` and
  `gen_ai.provider.name` identify GenAI operations; recommended response-chain
  fields include `gen_ai.response.id` and `gen_ai.request.previous_response.id`.
  `gen_ai.conversation.compacted=true` is a positive compaction indicator and
  should be left unset, not set false, when compaction is not reliably known.
  `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens` are recommended
  counts. Content fields such as `gen_ai.input.messages` and
  `gen_ai.output.messages` are opt-in and may contain sensitive information.
- OpenTelemetry Specification 1.59.0 tracing API (read 2026-08-11): a span has
  zero or one parent and an ordered list of links to same- or cross-trace span
  contexts; each link may carry attributes. P06 preserves those genuine link
  fields instead of encoding response ancestry as a homegrown timestamp shape.

Primary links:

- https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-spans.md
- https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md
- https://opentelemetry.io/docs/specs/otel/trace/api/

### Official provider fields used by the isolated adapter

The OpenAI Responses compaction guide (read 2026-08-11; unversioned live docs)
documents `context_management` with `type=compaction` and `compact_threshold`, an
opaque output item with `type=compaction`, response `id`, and request
`previous_response_id`. The adapter may recognize only explicitly documented
`grotto.openai.*` transport attributes that mirror those official fields. These
attribute names are a Grotto experimental adapter contract, not OTel standards.

Primary link:

- https://developers.openai.com/api/docs/guides/compaction

## Design inferences

1. A confirmed boundary requires positive evidence:
   `gen_ai.conversation.compacted=true`, a supported provider compaction item, or
   an explicit synthetic fixture label. A configured threshold alone means
   `armed`, not `compacted`.
2. Response continuity uses `gen_ai.request.previous_response.id` and
   `gen_ai.response.id`. A real OTel span link corroborates or, when it targets a
   known response span, structurally supplies ancestry with link provenance.
3. Missing IDs, truncated attributes/links, contradictory values, and absent
   token counts remain `UNKNOWN` or become explicit warnings. They are never
   silently repaired.
4. A context-reset indicator may report that input tokens decreased across a
   positively confirmed compaction boundary. This is a structural discontinuity,
   not evidence of lost meaning, worse answers, or semantic degradation.
5. Answer drift is `UNKNOWN` unless both sides provide the same kind of synthetic
   label, hash, or structural fingerprint. Grotto compares supplied values but
   never derives them from messages.

## Versioned report contract

`grotto.compaction_report.v1` is deterministic: no generation timestamp, random
identifier, environment path, or transcript content. It contains the trace ID,
source span count, ordered observations, provenance for every normalized signal,
boundary-local token values/deltas with known/unknown states, chain status,
context-reset status, supplied-fingerprint comparison, and warnings.
The normative JSON shape is checked in at
`schemas/compaction-report-v1.schema.json`.

Ordering is response-chain topological order where the chain is unambiguous,
then span start time and span ID as stable tie-breakers. Branches are preserved
and flagged. Broken or cross-trace links do not fabricate ancestors.

## Product surface and demo

- `grotto compaction <trace-id>` loads an already-redacted local SQLite trace.
- `grotto compaction --otlp-json <fixture>` imports a synthetic OTLP export
  request in memory through the existing OTLP mapper and renders the same report.
- `--json` emits the versioned machine-readable contract; text mode emits a
  compact chain/timeline view.

The five-minute demo uses only the checked-in synthetic fixture, a temporary
local database when persistence is demonstrated, and the locally built binary.
No live model, credential, private transcript, cloud service, or outbound network
is involved.

## Failure and privacy behavior

- OTLP JSON must be syntactically valid and contain exactly one trace for direct
  fixture rendering; malformed or ambiguous input fails with a wrapped error.
- Only whitelisted structural attributes are read. Message/system/tool content is
  ignored even when present. Imported fixtures pass through the same redaction
  function before normalization.
- Negative token values, invalid booleans, duplicate response IDs, conflicting
  standard/provider evidence, dropped fields, and broken links are reported
  deterministically without panic.
- The implementation has no HTTP client, exporter, model SDK, credentials, or
  background goroutine.
