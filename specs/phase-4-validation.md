# Phase 4 Validation — Compare, Polish, Distribution

Pass/fail conditions. Read at phase completion, not during implementation.

- [ ] PASS iff `grotto diff <id-a> <id-b>` prints per-span duration deltas across two runs.
- [ ] PASS iff `grotto list` shows the last 50 runs newest-first with label, span count, duration, source, age.
- [ ] PASS iff `make build` cross-compiles darwin/arm64 + linux/amd64 static binaries, each under 25 MB.
- [ ] PASS iff `file dist/grotto-darwin-arm64` reports a Mach-O 64-bit executable.
- [ ] PASS iff `README.md` contains the architecture diagram + an explicit "OTel data model + Go idioms learned" section.
- [ ] PASS iff redaction tests turn all 4 known secret patterns into `‹redacted›`.

FAIL on any unchecked box → fix before tagging v1.0. This is the career-facing artifact gate.
