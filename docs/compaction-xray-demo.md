# Compaction X-Ray: five-minute local demo

This demo is deterministic and local. It uses one checked-in synthetic OTLP JSON
export request, makes no network call, starts no receiver, reads no credential or
private transcript, and does not call a model.

## 1. Build the pure-Go binary

From the Grotto repository root:

```bash
CGO_ENABLED=0 go build -o /tmp/grotto-p06 ./cmd/grotto
```

## 2. Render the compact timeline

```bash
/tmp/grotto-p06 compaction \
  --otlp-json tests/fixtures/compaction/one_compaction.otlp.json
```

Expected output:

```text
Compaction X-Ray v1  trace 11111111111111111111111111111111  3 source spans
01 [?] resp_before          root                     in=10000 out=500 Δin=UNKNOWN reset=unknown drift=unknown
02 [COMPACT] resp_after           linked←resp_before       in=3500 out=550 Δin=-6500 reset=detected drift=changed
claim ceiling: structural indicators only; no semantic-quality claim
```

What to notice:

- The OTLP fixture serializes the compacted span before its predecessor, but the
  report reconstructs `resp_before -> resp_after` deterministically.
- A genuine OTel link corroborates the `previous_response` attribute.
- `Δin=-6500` is obvious at the confirmed boundary. `reset=detected` means only
  that the input token count decreased there.
- `drift=changed` compares the two supplied synthetic hashes. It does not inspect
  an answer or claim semantic degradation.

## 3. Inspect the versioned export

```bash
/tmp/grotto-p06 compaction \
  --otlp-json tests/fixtures/compaction/one_compaction.otlp.json \
  --json
```

The JSON starts with `"schema": "grotto.compaction_report.v1"` and contains
field-level provenance, known/unknown token states, chain status, warnings, and
the explicit structural-only claim ceiling. It contains no generation timestamp,
environment path, prompt, message, tool argument, or output content, so identical
input produces identical output.

## 4. Analyze an ingested trace (optional)

If a synthetic GenAI trace is already in Grotto's local SQLite store:

```bash
/tmp/grotto-p06 compaction <trace-id>
```

This uses the same normalization and renderer. OTLP ingest redacts credential
shapes before persistence; direct fixture import applies that same redaction
before analysis.
