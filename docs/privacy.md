# Privacy and trace redaction

Grotto is local-first, but local telemetry can still contain credentials, session tokens, personal identifiers, prompts, completions, tool payloads, exception text, and file paths. P08 treats redaction as a disclosure boundary, not as a compliance certificate.

## Safe defaults

- Both marks and OTLP ingest pass through one versioned evaluator before SQLite.
- `grotto redact-preview` evaluates an imported or stored trace without mutating it.
- Preview never prints retained raw values. It reports path, category, matched rule, action, original type/byte length, and a masked preview or versioned digest.
- There is no reveal mode. Raw values are never copied into logs, reports, reversible maps, encryption envelopes, or secret escrow.
- Authorization headers, token/cookie attributes, credential-shaped strings, emails, home-user paths, and URL query strings are masked by default.
- GenAI messages, prompts, completions, system instructions, tool arguments, and tool results are dropped by default.
- Binary values are represented by a stable domain-separated SHA-256 digest. Hashing is not anonymization for small or predictable input spaces.
- Declared JSON that is malformed or exceeds the nesting bound is `UNKNOWN` and fails closed.

## Boundaries

The default policy is a conservative local design informed by official OpenTelemetry guidance; OpenTelemetry cannot determine what is sensitive in a specific application. Operators remain responsible for consent, legal requirements, destination controls, and reviewing custom attributes.

A successful fixture preview proves deterministic local behavior only. It does not prove that every instrumentation library, exporter, collector, backend, or future field uses the same policy.

See [the policy guide](redaction-policy.md) and [the P08 design evidence](design/phase-p08-trace-redaction-preview.md).
