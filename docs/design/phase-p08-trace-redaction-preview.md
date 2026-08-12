# P08: Trace Redaction Preview

Status: selected for implementation

As of: 2026-08-11

Starting binding: `origin/main@6d40420bd2c6c435847cd68758c3858df8053eaf`

## Product outcome

An operator can point Grotto at a synthetic stored or imported trace and receive a deterministic field-by-field disclosure plan before export or sharing. The plan says which fields are retained, masked, hashed, truncated, or dropped, why each action was selected, and where the governing rule came from. Preview never changes the source file or SQLite database, and raw content is off with no reveal flag.

P08 extends the existing pre-persistence path:

```text
marks or OTLP -> model.Trace -> canonical redaction evaluator -> InsertTrace -> SQLite
                                  |
stored trace (read-only) ----------+-> safe preview report
imported JSON file ----------------+
```

There is one evaluator. Preview consumes its decisions; ingest consumes its transformed copy. No reversible secret escrow, decryptable mapping, or hidden raw-value report is created.

## Ownership and collision boundary

- Feature module: `internal/redaction/**`.
- Policy/schema surfaces: `internal/redaction/default_policy_v1.json`, `schemas/redaction-policy-v1.schema.json`, and `schemas/redaction-preview-report-v1.schema.json`.
- Tests and fixtures: `internal/redaction/**_test.go`, `internal/cli/redact_preview_test.go`, `internal/store/redact_test.go`, and `tests/fixtures/redaction/**`.
- Docs: this design, `docs/privacy.md`, `docs/redaction-policy.md`, `docs/demo-redaction-preview.md`, and minimal README/roadmap links.
- Shared integration: minimal edits to `internal/store/redact.go`, `internal/store/queries.go`, `internal/store/sqlite.go`, `internal/cli/root.go`, and one new CLI command.
- P06 compaction visualization and P09 token/cache ledgers are explicitly excluded.

## Current Grotto audit

The existing implementation has the correct architectural chokepoint but a narrow policy:

- `internal/store.InsertTrace` calls `store.Redact` before beginning the SQLite transaction. Both marks and OTLP already converge there.
- `store.Redact` returns a deep copy and masks four value shapes: AWS access key IDs, GitHub classic PATs, OpenAI-style keys, and Slack tokens.
- It evaluates run labels, root names, span names, and attribute values. Attribute keys are not transformed.
- It has no policy version, rule provenance, per-field explanation, preview report, URL/PII/GenAI classification, drop/hash/truncate actions, nested JSON handling, or `UNKNOWN` state.
- `store.Open` creates directories and applies schema, so it cannot be used for a byte-stable dry-run. P08 requires a separate read-only open path that performs no migration.

The implementation will therefore preserve `InsertTrace` as the single applied boundary, replace its narrow internal matcher with the canonical evaluator, and keep regression tests for every legacy credential pattern.

## Current official OpenTelemetry guidance

Only primary OpenTelemetry sources are treated as standards evidence. Local fixtures and action choices below are design inferences, not claims that OTel mandates Grotto's exact policy.

### Standard requirements and guidance

- OpenTelemetry Specification v1.60.0 (released 2026-08-07) says SDK attributes must follow common attribute limits. The common specification defaults the top-level attribute count to 128 and the attribute value length to infinity, while defining recursive truncation behavior when a value-length limit is configured. Grotto's finite preview bounds are a local safety choice, not an OTel default. Sources: [common attribute limits](https://opentelemetry.io/docs/specs/otel/common/#attribute-limits), [span limits](https://opentelemetry.io/docs/specs/otel/trace/sdk/#span-limits), [v1.60.0 release](https://github.com/open-telemetry/opentelemetry-specification/releases/tag/v1.60.0).
- OpenTelemetry's sensitive-data guidance says implementers are responsible for protecting credentials, session tokens, PII, financial/health data, and user-behavior data; it recommends data minimization and describes deleting, hashing, truncating, and redacting attributes. It also warns that hashes of small or predictable input spaces can be reversed in practice. Source page last modified 2026-01-14 at commit `4edfbfc2`: [handling sensitive data](https://opentelemetry.io/docs/security/handling-sensitive-data/).
- The official Collector redaction processor uses fail-closed allowlists, masked blocked keys/values, explicit precedence, hashing options, and URL/database sanitizers. At the 2026-08-11 research boundary, Collector Contrib `main` was `167863ba096e22fe6492ed949fe34944c155dd42`. This is reference behavior, not a compatibility promise: [redaction processor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/processor/redactionprocessor/README.md).
- The dedicated GenAI semantic-conventions repository marks `gen_ai.system_instructions`, `gen_ai.input.messages`, `gen_ai.output.messages`, and `gen_ai.tool.definitions` as opt-in content; it warns that message content can contain sensitive user/PII data and allows filtering or truncation. At the research boundary, its `main` was `8d3e4a0f3c34a46f6edb9c71e8666e02e6bf3958`: [GenAI agent spans](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md).

### Grotto design inferences

- Preview is raw-content-off. Even a `retain` decision renders only a placeholder and metadata, never the candidate value.
- GenAI prompts, completions, system instructions, tool arguments/results, and memory records are dropped by the safe default policy because they are opt-in content and likely to carry sensitive data.
- Authorization headers, tokens, cookies, and credential-shaped values are masked. Email addresses, home-user path components, and URL query strings are masked by conservative patterns.
- Exception messages are secret-scanned and length-bounded. Oversized values are truncated only after sensitive substrings are removed; binary values use a stable SHA-256 digest. Digests are identifiers, not anonymization guarantees.
- Malformed JSON on a path declared as JSON, or content beyond the configured depth bound, is `UNKNOWN` and fails closed by dropping the affected attribute/subtree.

### Local fixture behavior

All committed examples are synthetic. Fixture matches demonstrate only the local deterministic contract. They do not prove ecosystem adoption, live exporter interoperability, runtime uptake, regulatory compliance, or production safety.

## Versioned contracts

### Policy V1

`grotto.redaction-policy.v1` contains:

- a policy ID and semantic version;
- evaluator bounds (`max_depth`, `max_value_bytes`);
- JSON-inspection path patterns;
- rules with unique IDs, priority, path glob, optional value regex, category, action, explanation, provenance, and action parameters.

Rule precedence is deterministic: highest numeric priority, then most-specific path (fewest wildcards and longest literal), then lexicographically smallest rule ID. Matching is case-insensitive for field paths and explicit for value regexes. The winning rule and precedence basis appear in every decision.

Actions are:

- `retain`: applied copy keeps the value; preview prints `<retained>`.
- `mask`: matching substrings or the whole field become `‹redacted›`.
- `hash`: the value becomes `sha256:v1:<hex>`; already-versioned digests are not rehashed.
- `truncate`: sensitive substrings are masked first, then the UTF-8-safe value is capped with an omission marker.
- `drop`: attributes are removed; scalar structural fields become empty.

### Preview Report V1

`grotto.redaction-preview.v1` contains policy provenance and digest, evaluator version, source kind/reference, trace ID, deterministic action totals, `UNKNOWN` totals, and path-sorted field decisions. Each decision contains:

- canonical structural path plus stable opaque references for untrusted attribute/JSON keys, and category;
- matched rule ID and rule provenance;
- action and explanation;
- original type and byte length;
- safe redacted preview or digest;
- `known` or `unknown` status plus a reason when unknown.

No original value, reveal token, encryption key, reversible map, or escrow reference is part of either contract.

## Non-mutation and network boundary

- Imported traces are decoded from an already-open read-only file descriptor and never rewritten.
- Stored traces resolve the configured path to an absolute SQLite file URI and use `mode=ro` with normal locking, busy handling, and change detection; the path must already exist. No directory creation or migration is permitted, previews do not write the database, and a pre-span-link schema remains readable with unavailable diagnostics and links represented as empty legacy data.
- Tests hash the source file and database plus sidecar presence/content before and after preview.
- The evaluator uses only Go's standard library and embedded policy bytes. It has no network client, provider, secret lookup, or environment-dependent rule fetch.

## Five-minute acceptance story

1. Copy the synthetic fixture and default policy to a temporary directory.
2. Hash the fixture and a prepared fixture database.
3. Run human and JSON previews for the imported trace and the stored trace.
4. Confirm the output includes every action and no sentinel secret string.
5. Re-hash the inputs and confirm byte-for-byte equality.
6. Run focused tests and the no-cgo build gate.

The exact commands live in `docs/demo-redaction-preview.md` once implementation lands.
