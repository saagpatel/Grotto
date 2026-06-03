# Phase 1 Validation — Marks Capture → Store → Static Waterfall

Pass/fail conditions. Read at phase completion, not during implementation.

- [ ] PASS iff `grotto run -- tests/fixtures/build-script.sh` prints "stored trace <id>".
- [ ] PASS iff `grotto show <id> --json` returns 6 spans with correct parent_span_id nesting (1 root + 5 marks).
- [ ] PASS iff `go test ./internal/render/... ./internal/collect/...` reports all tests passing.
- [ ] PASS iff `grotto show <id>` output matches `tests/fixtures/expected-waterfall.txt` byte-for-byte.
- [ ] PASS iff layout bar offsets match expected within ±1 char on the 6-span fixture.

FAIL on any unchecked box → fix before advancing to Phase 2. This is the first shippable checkpoint (60% of personal-use value).
