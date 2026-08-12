# Compaction X-Ray fixtures

All fixture content is synthetic. `one_compaction.otlp.json` is an OTLP JSON
`ExportTraceServiceRequest` with a root span, two GenAI client spans, one genuine
OTel span link, a confirmed compaction flag, token counts, and caller-supplied
structural hashes. The spans are intentionally serialized out of timestamp order.

`internal/compaction/report_test.go` supplies the deterministic scenario matrix
using the same OTel-shaped `model.Span` contract:

- no compaction;
- one and repeated compactions;
- missing previous-response ancestry;
- branched response chains;
- out-of-order spans;
- missing token data;
- structural context-window reset;
- dropped/truncated attributes and links;
- experimental provider-extension fields;
- conflicting link and attribute ancestry;
- invalid token and nonconformant compaction fields;
- supplied answer labels/hashes and absent-drift evidence.

`malformed.otlp.json` is intentionally invalid JSON. The golden text report is
`expected_one_compaction.txt`. None of these files contains a private transcript,
credential, model output, or network dependency.
