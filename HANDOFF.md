# Grotto — Session Handoff

## Status
**COMPLETE / publicly released through v1.8.3.** v1 -> v1.8.3 are on `main`, thirteen
GitHub releases are tagged, and `v1.8.3` is the latest public release with macOS
arm64 and Linux amd64 binaries plus checksums. The downloaded release binary reports
`grotto v1.8.3`.

## Shipped This Session (2026-06-06) — v1.3 -> v1.8
- **v1.4.0** — the cargo build adapter (`grotto run --adapter=cargo` -> per-crate
  waterfall from cargo's stable `--timings` `UNIT_DATA`; rollup bucket, `--limit`,
  build-script disambiguation).
- **v1.5.0** — cargo-analysis features: `grotto show --critical-path`, `grotto diff
  --sort=delta`, and `grotto show --sections`.
- **v1.6.0** — the go-test adapter (`grotto run --adapter=go-test -- go test ./...`)
  parses the `-json` stream into package -> test spans.
- **v1.7.0** — streaming adapter input for stdout-consuming adapters. `go-test`
  now consumes high-volume `-json` streams line by line instead of buffering the
  whole stream in memory.
- **v1.8.0** — the JUnit XML adapter (`grotto run --adapter=junit -- python3 -m
  pytest`) turns pytest/JUnit XML reports into suite -> test waterfalls. This is
  the third adapter and the universal test-result format bridge.

Every release followed the same shape: design-first, review-gated before merge,
and live/dogfood verified. Public release artifacts are attached to GitHub
releases, with the v1.8 line as the clean JUnit demo target. The `v1.8.1` patch
adds the existing-artifact `--junit-file=PATH` import path; `v1.8.2` makes
`grotto show --json` render OTel `kind`/`status` as readable labels; `v1.8.3`
shortens go-test package span labels by trimming the local module prefix.

## Current Public Demo Path
1. Open the latest release: https://github.com/saagpatel/Grotto/releases/tag/v1.8.3
2. Download the static binary for the target platform and verify it against
   `checksums.txt`.
3. Run `./grotto --version` and expect `grotto v1.8.3`.
4. Demo the newest adapter with:

```bash
./grotto run --adapter=junit -- python3 -m pytest
./grotto show <trace-id>
```

If pytest is unavailable or the report already exists, import a CI artifact instead:

```bash
./grotto run --adapter=junit --junit-file=reports/junit.xml -- true
./grotto show <trace-id>
```

## Key Decisions (do not revisit without new evidence)
- Adapters are pluggable via a `map[string]Adapter` registry + `adapter.Names()`.
  Current adapters: `cargo`, `go-test`, `junit`.
- `Adapter.CapturesStdout()` decides whether Run captures stdout silently. Streaming
  adapters additionally implement `StreamAdapter`; `go-test` uses this path.
- File/report adapters receive a per-run `ScratchDir`; `junit` either injects
  `--junitxml` into that scratch dir or reads an explicit `--junit-file=PATH`.
- cargo: stable `--timings` HTML `UNIT_DATA` (not nightly JSON); DAG edges as
  `cargo.unit`/`cargo.unblocks`; sub-phases as `cargo.section`.
- go-test: `-json` event stream -> package/test spans, microsecond `Time` fields,
  local module prefix trimmed from package labels, error status on fail, incomplete
  streams close at run end.
- junit: pytest-first `--junitxml` injection; JUnit durations are real but start
  times are synthesized sequentially within suites because JUnit XML has no per-test
  start timestamps. Explicit-file imports expand the root span to fit report
  durations, so `-- true` imports do not squash the waterfall.
- Rollup, gaps, sections-hiding are render-only; the store keeps every span. TUI
  shows everything; static `show` curates.

## Verification (current as of this handoff)
`CGO_ENABLED=0 go build ./...` passed · `go test ./...` passed · `golangci-lint run
./...` passed with 0 issues · live go-test smoke confirmed shortened package labels ·
downloaded `v1.8.3` darwin release binary reported `grotto v1.8.3` · README now
includes a small JUnit demo screenshot asset.

## Next Candidates
1. Consider a short recorded GIF only if motion would make the public demo clearer.
