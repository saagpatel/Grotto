# Grotto — Session Handoff

## Status
**COMPLETE / publicly released through v1.8.0.** v1 -> v1.8.0 are on `main`, ten GitHub
releases are tagged, and `v1.8.0` is the latest public release with macOS arm64 and
Linux amd64 binaries plus checksums. `main` is at the v1.8.0 merge commit `0edc0a8`.
After fetching tags, `git describe --tags --always --dirty --long` reports
`v1.8.0-0-g0edc0a8`; the downloaded release binary reports `grotto v1.8.0`.

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
releases, with `v1.8.0` as the clean demo target.

## Current Public Demo Path
1. Open the latest release: https://github.com/saagpatel/Grotto/releases/tag/v1.8.0
2. Download the static binary for the target platform and verify it against
   `checksums.txt`.
3. Run `./grotto --version` and expect `grotto v1.8.0`.
4. Demo the newest adapter with:

```bash
./grotto run --adapter=junit -- python3 -m pytest
./grotto show <trace-id>
```

## Key Decisions (do not revisit without new evidence)
- Adapters are pluggable via a `map[string]Adapter` registry + `adapter.Names()`.
  Current adapters: `cargo`, `go-test`, `junit`.
- `Adapter.CapturesStdout()` decides whether Run captures stdout silently. Streaming
  adapters additionally implement `StreamAdapter`; `go-test` uses this path.
- File/report adapters receive a per-run `ScratchDir`; `junit` injects `--junitxml`
  into that scratch dir and reads the report back from the same path.
- cargo: stable `--timings` HTML `UNIT_DATA` (not nightly JSON); DAG edges as
  `cargo.unit`/`cargo.unblocks`; sub-phases as `cargo.section`.
- go-test: `-json` event stream -> package/test spans, microsecond `Time` fields,
  error status on fail, incomplete streams close at run end.
- junit: pytest-first `--junitxml` injection; JUnit durations are real but start
  times are synthesized sequentially within suites because JUnit XML has no per-test
  start timestamps.
- Rollup, gaps, sections-hiding are render-only; the store keeps every span. TUI
  shows everything; static `show` curates.

## Verification (current as of this handoff)
`CGO_ENABLED=0 go build ./...` passed · `go test ./...` passed · `golangci-lint run
./...` passed with 0 issues · local tags fetched through `v1.8.0` · downloaded
`v1.8.0` darwin release binary reported `grotto v1.8.0`.

## Next Candidates
1. Add a tiny README GIF/screenshot or terminal transcript for the JUnit demo.
2. Add `--junit-file=PATH` to read an existing CI artifact without re-running tests.
3. Polish `grotto show --json` kind/status readability (`server`, `error` instead of
   raw enum ints).
4. Shorten go-test package names by trimming the common module prefix.
