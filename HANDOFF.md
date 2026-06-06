# Grotto — Session Handoff

## Status
**COMPLETE / shipped through v1.6.0.** v1 → v1.6.0 all on `main`, nine GitHub releases tagged
(…v1.4.0, v1.5.0, **v1.6.0**), CI gating main. Nothing in flight, working tree clean (only this
untracked `HANDOFF.md`). `main` at the v1.6.0 merge commit `621eb0c`. `grotto --version` → `v1.6.0`.

## Shipped This Session (2026-06-06) — four releases, v1.3 → v1.6
- **v1.4.0** — the cargo build adapter (`grotto run --adapter=cargo` → per-crate waterfall from
  cargo's stable `--timings` `UNIT_DATA`; rollup bucket, `--limit`, build-script disambiguation).
- **v1.5.0** — three cargo-analysis features: `grotto show --critical-path` (longest dependency
  chain / build floor via memoized longest-path DFS over `cargo.unit`/`cargo.unblocks` attributes),
  `grotto diff --sort=delta` (per-crate cache deltas ranked by impact), `grotto show --sections`
  (frontend/codegen sub-phase split, opt-in).
- **v1.6.0** — the **go-test adapter**, proving the `Adapter` registry seam generalizes (a second
  adapter is one file + one registry line). `grotto run --adapter=go-test -- go test ./...` parses
  the `-json` stream into package→test spans. The interface gained `CapturesStdout()` (go test
  -json is on stdout, captured silently; cargo's stderr path unchanged) + `BuildContext.Stdout`.

Every release: design-first (probe-corrected premises), `/code-review`-gated before merge
(v1.4: 3 fixes, v1.5: 4, v1.6: 4), live/dogfood-verified end-to-end.

## Next Step (v1.7 candidates — design-first as usual)
1. **Streaming adapter input** — `go test ./...` on a huge repo buffers the whole `-json` stream in
   memory before `ParseSpans`. A streaming parse (feed the adapter as output arrives, not after)
   bounds memory. Deferred from v1.6; the one known scaling limit of the adapter mechanism.
2. **A third adapter** (pytest `--json-report`, or jest `--json`) — further validates the seam and
   broadens reach beyond Rust/Go.
3. **rmeta pipelining in critical-path** — fold `unblocked_rmeta_units` into the DAG for a more
   accurate cargo build floor on large builds (deferred from v1.5).
4. **`grotto show --json` kind/status readability** — raw ints (2, 3) → OTel names (`server`,
   `error`). Small long-deferred polish.
5. **go-test package-name shortening** — package spans show full import paths; trimming the common
   module prefix (→ `internal/render`) would tidy the waterfall. Cosmetic.

## Key Decisions (do not revisit)
- Adapters are pluggable via a `map[string]Adapter` registry + `adapter.Names()`; cargo + go-test.
  `Adapter.CapturesStdout()` decides whether Run captures stdout silently (go-test) or passes it
  through (cargo, which reads stderr). Adapter parse failure is non-fatal (warn + store base trace).
- cargo: stable `--timings` HTML `UNIT_DATA` (NOT nightly json); DAG edges as `cargo.unit`/
  `cargo.unblocks` attributes; sub-phases as `cargo.section`. Critical-path = render-time analysis
  via memoized DFS (robust to ties/cycles). go-test: `-json` event stream → package→test spans,
  microsecond `Time` fields, error status on fail, incomplete streams close at run end.
- Rollup, gaps, sections-hiding are render-only; the store keeps every span. TUI shows everything;
  static `show` curates. Conflicting `show` view flags are mutually exclusive (cobra).
- Release flow: build at the merge commit via `go build -o dist/grotto-<os>-<arch>` with ldflags
  `-X …/internal/cli.version=vX.Y.Z` (prefer direct `go build` over `make build-all` for re-cuts —
  make's file-targets skip stale binaries and `rm -rf dist` is token-gated; `go build` overwrites).
  Then `gh release create vX.Y.Z --target main` with binaries + checksums; merge PRs server-side via
  `gh pr merge --merge` (push-to-main hook). Verify `main` via `gh api`, not local refs.

## Verification (all green at handoff)
`CGO_ENABLED=0 go build ./...` · `go test ./...` · `golangci-lint run ./...` (0 issues) ·
CI green on PR #10 · `/code-review` clean after fixes · `grotto --version` → `v1.6.0` ·
cargo + go-test adapters, critical-path, diff --sort=delta, --sections all e2e/dogfood-confirmed.
