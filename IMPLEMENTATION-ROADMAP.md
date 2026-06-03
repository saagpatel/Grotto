# Grotto — Implementation Roadmap

Full architecture + phased build plan. CLAUDE.md is identity; this is the build reference. Source of truth for decisions: `IMPLEMENTATION-PLAN.md` (Sections 1–6).

## Architecture

### System Overview

```
  CAPTURE SOURCES                  SHARED SPINE                 PRESENTATION
  ───────────────                  ────────────                 ────────────
  ./script.sh + grotto mark ─UDS─┐
                                 ▼
                         [ span ingest API ]
  instrumented app ─gRPC :4317──►├─map proto─► [ OTel span model ] ─► [ SQLite store ]
  OTLP exporter    ─HTTP :4318──►┘             (trace/span/attr)            │
                                                                  ┌─────────┴────────┐
                                                            [ layout.go ]      [ Bubble Tea TUI ]
                                                          (proportional bars)  (list/waterfall/inspector)
                                                                  │
                                                          [ grotto show ] (static waterfall)
```

Both capture paths (demarcated marks + OTLP receiver) converge on one OpenTelemetry span model, one SQLite store, and one rendering layer (`layout.go`) shared by the static printer and the interactive TUI.

### File Structure

```
Grotto/
├── cmd/grotto/main.go              # entrypoint; wires cobra root
├── internal/
│   ├── cli/{root,run,mark,serve,show,list,diff,tui}.go
│   ├── model/{span.go,span_test.go}        # OTel-shaped structs + AssembleTree
│   ├── collect/{marks.go,marks_test.go}    # UDS/spool collector (model B)
│   ├── otlp/{grpc.go,http.go,mapproto.go,mapproto_test.go}  # receiver (model C)
│   ├── store/{sqlite.go,queries.go,redact.go,store_test.go} # SQLite + redaction
│   ├── render/{layout.go,layout_test.go,waterfall.go}       # bars + static printer
│   └── tui/{app.go,runlist.go,waterfall.go,inspector.go}    # Bubble Tea screens
├── tests/fixtures/{three-span-trace.json,build-script.sh,expected-waterfall.txt,secrets-attrs.json,three-span-trace-slow.json}
├── specs/phase-{0,1,2,3,4}-validation.md
├── migrations/001_init.sql         # embedded via go:embed
├── go.mod / go.sum / .golangci.yml
├── progress.json / tests.json      # created in Phase 0
└── CLAUDE.md
```

### Data Model

```sql
CREATE TABLE traces (
    trace_id    TEXT PRIMARY KEY,
    run_label   TEXT NOT NULL,
    source      TEXT NOT NULL,            -- 'mark' | 'otlp'
    root_name   TEXT NOT NULL,
    started_ns  INTEGER NOT NULL,
    ended_ns    INTEGER NOT NULL,
    duration_ns INTEGER NOT NULL,
    span_count  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE TABLE spans (
    span_id        TEXT PRIMARY KEY,
    trace_id       TEXT NOT NULL REFERENCES traces(trace_id) ON DELETE CASCADE,
    parent_span_id TEXT,                  -- NULL for root
    name           TEXT NOT NULL,
    kind           INTEGER NOT NULL,      -- OTel SpanKind 0..5
    status_code    INTEGER NOT NULL,      -- 0 unset, 1 ok, 2 error
    started_ns     INTEGER NOT NULL,
    ended_ns       INTEGER NOT NULL,
    duration_ns    INTEGER NOT NULL
);
CREATE TABLE span_attributes (
    span_id    TEXT NOT NULL REFERENCES spans(span_id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_type TEXT NOT NULL,             -- 'str'|'int'|'float'|'bool'
    value      TEXT NOT NULL,
    PRIMARY KEY (span_id, key)
);
CREATE INDEX idx_spans_trace    ON spans(trace_id);
CREATE INDEX idx_spans_parent   ON spans(parent_span_id);
CREATE INDEX idx_traces_started ON traces(started_ns DESC);
CREATE INDEX idx_attr_span      ON span_attributes(span_id);
```

### Type Definitions

```go
// internal/model/span.go
package model

type SpanKind int32   // 0 Unspecified .. 5 Consumer (mirrors OTel)
type StatusCode int32 // 0 Unset, 1 Ok, 2 Error

type Attribute struct {
    Key       string
    ValueType string // "str"|"int"|"float"|"bool"
    Value     string // stringified; type-assert on ValueType
}
type Span struct {
    SpanID, TraceID, ParentSpanID, Name string
    Kind       SpanKind
    Status     StatusCode
    StartedNs, EndedNs, DurationNs int64
    Attributes []Attribute
}
type Trace struct {
    TraceID, RunLabel, Source, RootName string
    StartedNs, EndedNs, DurationNs int64
    SpanCount int
    Spans     []Span
}
type TreeNode struct {
    Span     Span
    Depth    int
    Children []*TreeNode
}
```

### API Contracts

Grotto makes ZERO outbound calls. Only inbound surface is the OTLP receiver:

| Service | Endpoint | Method | Auth | Rate Limit | Pagination | Purpose |
|---------|----------|--------|------|------------|------------|---------|
| OTLP/gRPC | `127.0.0.1:4317` `…trace.v1.TraceService/Export` | gRPC unary | None (loopback bind) | None; buffered channel cap 1024 | N/A (streamed ResourceSpans) | Ingest spans from any OTel-instrumented program |
| OTLP/HTTP | `127.0.0.1:4318/v1/traces` | POST | None (loopback bind) | None (local) | N/A (single protobuf body) | HTTP/protobuf fallback ingest path |

### Dependencies

```bash
go get github.com/spf13/cobra@v1.8.1
go get github.com/charmbracelet/bubbletea@v0.27.1
go get github.com/charmbracelet/lipgloss@v0.13.0
go get github.com/charmbracelet/bubbles@v0.20.0
go get go.opentelemetry.io/otel@v1.30.0
go get go.opentelemetry.io/otel/sdk@v1.30.0
go get go.opentelemetry.io/proto/otlp@v1.3.1
go get google.golang.org/grpc@v1.66.0
go get google.golang.org/protobuf@v1.34.2
go get modernc.org/sqlite@v1.33.0
go get github.com/stretchr/testify@v1.9.0   # dev
brew install go golangci-lint               # system; go 1.22+, lint v1.60+
brew install otel-cli                        # optional, Phase 2 receiver testing
```

## Scope Boundaries

**In scope (v1):** marks capture (`grotto mark`), OTLP receiver (gRPC + HTTP), shared OTel span model, SQLite history, static waterfall, interactive Bubble Tea TUI (3 screens), `diff`/`list`, secret redaction on ingest, single static binary.
**Out of scope:** cloud export, multi-machine aggregation, auth on the receiver, encryption at rest, hosted UI, sampling/retention policies.
**Deferred:** gRPC-only-fails → HTTP-only receiver (v1.1 gRPC); span-nesting for marks via `--child` (v1.1); linux/amd64 release if cross-compile snags (darwin/arm64 first).

## Security and Credentials

- **Credential storage:** No credentials in scope — local-only, authenticates nothing. Receiver binds `127.0.0.1`, accepts unauthenticated spans by design.
- **Data boundaries:** Nothing leaves the machine. DB at `~/.grotto/grotto.db`.
- **Encryption:** None in v1 (relies on FileVault); deliberate decision to preserve single-binary/no-config.
- **Token rotation:** N/A — no tokens anywhere.
- **Sensitive data:** Redaction pass on ingest (`internal/store/redact.go`) replaces AWS keys (`AKIA[0-9A-Z]{16}`), GitHub PATs (`ghp_[0-9A-Za-z]{36}`), `sk-…`, and `xox[baprs]-` matches in span names + attribute values with `‹redacted›` before persistence; unit-tested against a secrets fixture.

---

## Phase 0: Toolchain + Scaffold + Span Model + Store (Week 1, first half)
**Objective:** Verified Go toolchain, Cobra skeleton with 7 stub subcommands, the shared OTel span model, and the pure-Go SQLite store with round-trip persistence. No UI, no receiver.
**Tasks:**
1. Verify toolchain + `go mod init github.com/saagar/grotto` + `.golangci.yml` — Acceptance: `go version` → go1.22+; `golangci-lint run ./...` exits 0.
2. Cobra root + 7 stub subcommands (run/mark/serve/show/list/diff/tui) — Acceptance: `go run ./cmd/grotto --help` lists all 7.
3. `internal/model/span.go` structs + `AssembleTree(spans) *TreeNode` — Acceptance: `go test ./internal/model/...` passes (6-span fixture → correct nesting/depths).
4. `internal/store/sqlite.go` with `go:embed` migration + insert/get queries — Acceptance: `go test ./internal/store/...` round-trips a 6-span trace identically; `CGO_ENABLED=0 go build ./...` succeeds.
5. Write `progress.json` + `tests.json` at root — Acceptance: `cat progress.json` / `cat tests.json` valid JSON; Phase 0 tasks marked done.
**Verification checklist:**
- [ ] `go version` → go1.22.x+
- [ ] `golangci-lint run ./...` → exits 0
- [ ] `go test ./internal/model/... ./internal/store/...` → all pass
- [ ] `CGO_ENABLED=0 go build ./cmd/grotto` → binary produced
- [ ] `cat progress.json` → valid JSON, Phase 0 tasks "done"
- [ ] `cat tests.json` → valid JSON, all phases listed
**Risks (2–4):**
- Transitive cgo dep breaks static build: gate `CGO_ENABLED=0 go build` in CI from day one → `go mod why` + replace offender.
- Go-idiom drift from Python/Rust habits: `.golangci.yml` (errcheck/govet/staticcheck) + `%w` error wrapping → phase-end idiom review against CLAUDE.md checklist.
**Parallel Dispatch Proposal:**
- Dispatchable in parallel: Task 3 (model), Task 4 (store) — after Task 1–2 scaffold.
- Subagent type: coder (Sonnet)
- Dispatch via: `claude agents` (v2.1.139+)
- Rationale: model + store share only the Span struct (fixed in Task 3); test suites disjoint.
- worktree note: Set `worktree.baseRef: "head"` in `.claude/settings.json` before dispatching (Phase 0 scaffolding is unpushed), or push the scaffolding branch first.
**Phase-end review:** Run `/ultrareview`. Address all findings before marking the phase complete.

## Phase 1: Marks Capture → Store → Static Waterfall (Week 1 second half → Week 2)
**Objective:** `grotto run -- <cmd>` collects `grotto mark` spans over `GROTTO_SOCK` into one trace; `layout.go` proportional-bar math (tested) + `grotto show <id>` static waterfall. First shippable checkpoint (60% of personal-use value).
**Tasks:**
1. `internal/collect/marks.go` — `run` opens UDS at `GROTTO_SOCK`, execs child with env set, assembles a Trace — Acceptance: `grotto run -- tests/fixtures/build-script.sh` stores a trace; `grotto show <id> --json` shows 6 spans, correct parents.
2. `grotto mark` — connects to `GROTTO_SOCK`, writes a span record — Acceptance: build-script.sh (5 marks) → exactly 5 child spans under 1 root.
3. `internal/render/layout.go` — pure fn TreeNode + width → per-span bar offset/width — Acceptance: `go test ./internal/render/...` matches expected offsets within ±1 char on 6-span fixture.
4. `internal/render/waterfall.go` + wire `grotto show <id>` — Acceptance: output matches `tests/fixtures/expected-waterfall.txt`.
**Verification checklist:**
- [ ] `grotto run -- tests/fixtures/build-script.sh` → "stored trace <id>"
- [ ] `grotto show <id> --json` → 6 spans, correct parent nesting
- [ ] `go test ./internal/render/... ./internal/collect/...` → all pass
- [ ] `grotto show <id>` → nested waterfall with proportional bars
**Risks (2–4):**
- UDS span plumbing flaky across shells: integration test under `sh -c`, assert span count → JSONL spool-file fallback (locked in plan 2d).
- Mark timing ambiguity (sibling vs nest): define sibling-by-default + `--child` flag, document in CLAUDE.md → sibling-only in v1.
**Parallel Dispatch Proposal:**
- Dispatchable in parallel: Task 1 (collector), Task 3 (layout math).
- Subagent type: coder (Sonnet)
- Dispatch via: `claude agents` (v2.1.139+)
- Rationale: collect + render/layout have no compile dependency; both depend only on `internal/model`.
- worktree note: Set `worktree.baseRef: "head"` if Phase 0 is unpushed, or push the branch first.
**Phase-end review:** Run `/ultrareview`. Address all findings before marking the phase complete.

## Phase 2: OTLP Receiver (gRPC + HTTP) (Week 3)
**Objective:** `grotto serve` runs a loopback OTLP receiver (gRPC `:4317` + HTTP `:4318`); `mapproto.go` maps protobuf ResourceSpans → model.Span into the same store. Hardest Go lift, isolated. Shippable: point any instrumented app at Grotto.
**Tasks:**
1. `internal/otlp/grpc.go` — register generated TraceService server, decode ExportTraceServiceRequest — Acceptance: `otel-cli span --endpoint localhost:4317 …` lands a trace; `grotto list` shows source=otlp.
2. `internal/otlp/mapproto.go` — proto spans → model.Span (hex IDs, kind, status, typed attrs) — Acceptance: `go test ./internal/otlp/...` maps `three-span-trace.json` to 3 spans, 1 attr/span preserved, parents intact.
3. `internal/otlp/http.go` — POST `/v1/traces` protobuf handler over shared map+store path — Acceptance: `curl --data-binary @fixture.pb localhost:4318/v1/traces` → trace stored.
4. Buffered-channel + single-writer goroutine between receiver and store — Acceptance: 200-span export stores with zero `database is locked` errors across 10 runs.
**Verification checklist:**
- [ ] `grotto serve` → binds `:4317` + `:4318` on 127.0.0.1
- [ ] `otel-cli span --endpoint localhost:4317 --name test` → in `grotto list`
- [ ] `go test ./internal/otlp/...` → mapping tests pass (count/parents/attrs)
- [ ] 200-span ingest → 200 spans stored, no lock errors
**Risks (2–4):**
- gRPC registration / proto version mismatch blocks phase: pin proto+grpc to roadmap versions, validate vs `otel-cli` early → ship HTTP-only, defer gRPC to v1.1 (locked fallback).
- Attribute type fidelity lost: `value_type` column + typed round-trip test → store raw protobuf JSON alongside for lossless re-read.
**Parallel Dispatch Proposal:**
- Dispatchable in parallel: Task 1 (gRPC server), Task 3 (HTTP handler) — after Task 2 mapping exists.
- Subagent type: coder (Sonnet)
- Dispatch via: `claude agents` (v2.1.139+)
- Rationale: both transports call shared `mapproto` but neither imports the other.
- worktree note: push the Phase 1 branch first, or set `worktree.baseRef: "head"` before dispatch.
**Phase-end review:** Run `/ultrareview`. Address all findings before marking the phase complete.

## Phase 3: Interactive Bubble Tea TUI (Week 4)
**Objective:** `grotto tui` launches a 3-screen Bubble Tea app — **Run List**, **Waterfall**, **Span Inspector** — with keyboard nav, expand/collapse, reusing `layout.go`.
**Tasks:**
1. `internal/tui/app.go` — Bubble Tea root model with screen-state enum + key routing — Acceptance: `grotto tui` launches, `q` quits cleanly, transitions work.
2. `internal/tui/runlist.go` (Screen 1) — list last 50 traces via store; enter selects — Acceptance: list shows label/span-count/duration; enter opens waterfall.
3. `internal/tui/waterfall.go` (Screen 2) — proportional waterfall from `layout.go`, scroll + expand/collapse — Acceptance: 200-span trace navigates at <50 ms/keystroke; collapse hides subtrees.
4. `internal/tui/inspector.go` (Screen 3) — focused span attributes/kind/status/timing — Acceptance: enter on a span shows typed attributes; esc returns to waterfall.
**Verification checklist:**
- [ ] `grotto tui` → run list renders, navigable
- [ ] enter on a run → waterfall with proportional bars
- [ ] expand/collapse + scroll <50 ms/keystroke on 200-span trace
- [ ] enter on a span → inspector typed attributes; esc returns
**Risks (2–4):**
- Waterfall layout off-by-one under variable width: `layout.go` already unit-tested in Phase 1; TUI only consumes it → indented-tree fallback.
- Bubble Tea perf on large traces: use `bubbles/viewport` windowed render, only visible rows → cap depth + lazy-expand.
**Parallel Dispatch Proposal:**
- Dispatchable in parallel: Task 2 (runlist), Task 4 (inspector) — after Task 1 root model.
- Subagent type: coder (Sonnet)
- Dispatch via: `claude agents` (v2.1.139+)
- Rationale: runlist + inspector share no state beyond root-model selection; waterfall (Task 3) is the sequential integration point.
- worktree note: push the Phase 2 branch first, or set `worktree.baseRef: "head"` before dispatch.
**Phase-end review:** Run `/ultrareview`. Address all findings before marking the phase complete.

## Phase 4: Compare, Polish, Distribution (Week 5)
**Objective:** `grotto diff <a> <b>` (per-span duration deltas) + `grotto list` table + static cross-compiled binaries + `README.md` with the OTel/Go learning writeup (the career artifact) + OTel fidelity polish (kind/status surfaced).
**Tasks:**
1. `grotto diff <a> <b>` — match spans by (name, depth, sibling-index), report deltas — Acceptance: `grotto diff <id-a> <id-b>` on two fixture runs prints +/- ms per matched span.
2. `grotto list` table (label, span count, duration, source, age) — Acceptance: `grotto list` shows last 50 runs newest-first.
3. `make build` cross-compiles darwin/arm64 + linux/amd64 static binaries — Acceptance: `file dist/grotto-darwin-arm64` → Mach-O 64-bit, <25 MB.
4. `README.md` with what/why + explicit "OTel data model + Go idioms learned" section — Acceptance: README has architecture diagram + OTel span-model writeup.
**Verification checklist:**
- [ ] `grotto diff <a> <b>` → per-span deltas printed
- [ ] `grotto list` → last 50 runs, newest first
- [ ] `make build` → 2 static binaries, each <25 MB
- [ ] `README.md` → architecture diagram + OTel/Go learning section
- [ ] redaction tests → 4 secret patterns become `‹redacted›`
**Risks (2–4):**
- Diff span-match ambiguity on repeated names: match by (name, depth, sibling-index) tuple, document → name-only with "multiple matches" warning.
- Cross-compile surfaces hidden cgo dep: `CGO_ENABLED=0` gated since Phase 0; `go mod why` offender → ship darwin/arm64 only for v1.
**Parallel Dispatch Proposal:**
- Dispatchable in parallel: Task 1 (diff), Task 2 (list), Task 4 (README).
- Subagent type: coder (Sonnet) for Task 1–2; research (Haiku) for Task 4 draft.
- Dispatch via: `claude agents` (v2.1.139+)
- Rationale: diff + list are independent query commands; README has no code dependency.
- worktree note: push the Phase 3 branch first, or set `worktree.baseRef: "head"` before dispatch.
**Phase-end review:** Run `/ultrareview`. Address all findings before marking the phase complete.
