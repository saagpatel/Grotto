# Phase 2 Validation — OTLP Receiver (gRPC + HTTP)

Pass/fail conditions. Read at phase completion, not during implementation.

- [ ] PASS iff `grotto serve` binds `:4317` (gRPC) and `:4318` (HTTP) on 127.0.0.1.
- [ ] PASS iff `otel-cli span --endpoint localhost:4317 --name test` results in `grotto list` showing that trace with source=otlp.
- [ ] PASS iff `go test ./internal/otlp/...` passes mapping tests (span count, parent links, attribute types preserved).
- [ ] PASS iff a 200-span export stores a trace with exactly 200 spans and zero "database is locked" errors across 10 runs.
- [ ] PASS iff the same `three-span-trace.json` ingested over `:4317` and `:4318` yields identical stored trees.

FAIL on any unchecked box → fix before advancing to Phase 3. Fallback path: HTTP-only receiver if gRPC blocks (defer gRPC to v1.1).
