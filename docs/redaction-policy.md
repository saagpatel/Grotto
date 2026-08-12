# Redaction policy V1

The embedded policy implements `grotto.redaction-policy.v1`. A custom policy can be inspected locally with:

```bash
grotto redact-preview --file tests/fixtures/redaction/synthetic-trace.json \
  --policy docs/examples/redaction-policy-v1.json --json
```

## Field paths

Rules match canonical, case-insensitive paths with `*` and `?` globs:

- `trace.run_label`
- `trace.root_name`
- `spans[0].name`
- `spans[0].attributes["authorization"]`
- `spans[0].attributes["payload"].json["nested"]["token"]`

Array indexes are concrete in reports and can be wildcarded in policy paths.

## Precedence

The winner is selected by highest numeric priority, most literal path characters, fewest wildcards, then lexicographically smallest rule ID. The report records that basis. This makes conflicting rules deterministic and lets a narrowly scoped allowlist beat a general pattern when its priority is explicitly higher.

## Actions

- `retain` keeps the applied value but preview shows only `<retained>`.
- `mask` replaces the matching substring, or the whole value when no value regex is supplied.
- `hash` emits `sha256:v1:<hex>` using a domain-separated SHA-256 digest. There is no key or escrow.
- `truncate` applies a UTF-8-safe byte bound.
- `drop` removes an attribute; structural strings become empty.

Policy metadata is trusted local configuration. Trace content is not. Use only synthetic values in committed policy examples, and do not place secrets in rule IDs, explanations, provenance, paths, or filenames.

Schemas: [`redaction-policy-v1.schema.json`](../schemas/redaction-policy-v1.schema.json) and [`redaction-preview-report-v1.schema.json`](../schemas/redaction-preview-report-v1.schema.json).
