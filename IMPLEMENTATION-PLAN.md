# Grotto — Implementation Plan

> Local-first observability for the terminal. OpenTelemetry-native trace waterfalls for shell commands, build scripts, and test suites. Zero cloud backend. First Go project — phased to learn Go idiom + the OTel data model deliberately.

---

## Section 1: EXEC SUMMARY

### 1a. What we're building

Grotto is a local-first, single-binary Go CLI + TUI that captures and renders OpenTelemetry trace waterfalls for shell commands, build scripts, and test suites entirely on the developer's machine. It captures spans two ways in v1: (B) **demarcated marks** — `grotto mark "step name"` calls inside the user's own scripts produce real OTel parent/child spans; and (C) an **OTLP receiver** — a local gRPC (`:4317`) + HTTP (`:4318`) endpoint that ingests spans from any already-instrumented program that points `OTEL_EXPORTER_OTLP_ENDPOINT` at it. Both paths converge on one OpenTelemetry span model, one SQLite store, and one interactive Bubble Tea TUI that renders nested timeline waterfalls and lets you query and compare past runs. The user is the operator (Saagar) profiling his own slow builds/tests; the problem it solves is replacing `echo "step 3" && date` guesswork with a real "where did the time go" waterfall.

### 1b. Riskiest parts and de-risking strategy

- **Risk: OTLP receiver complexity is the steepest Go lift for a first-timer (gRPC service registration + protobuf span decoding).**
  - Severity: HIGH
  - Why it is risky: The OTLP gRPC contract requires implementing a generated service interface and correctly mapping protobuf `ResourceSpans` → internal span model; a wrong mapping silently drops nesting or attributes, and gRPC server lifecycle in Go is unfamiliar territory.
  - Mitigation: Isolate the receiver as its own phase (Phase 2) that runs only AFTER the shared span model + store + marks pipeline already work; validate against a real exporter (`otel-cli` or a 20-line Python script) emitting a known 3-span trace, asserting the stored tree matches byte-for-byte on span count, parent links, and one attribute.
  - Fallback: Ship v1 with the HTTP/protobuf receiver only (simpler `net/http` handler, no gRPC service registration) and defer gRPC `:4317` to v1.1; the HTTP endpoint `:4318` satisfies the "point an instrumented app at it" demo.

- **Risk: First Go project — idiom mistakes (error handling, goroutine lifecycle, context propagation) compound into rework.**
  - Severity: MEDIUM
  - Why it is risky: Go's explicit error returns, channel/goroutine concurrency, and `context.Context` plumbing differ sharply from Python/Rust habits; getting span timing wrong under concurrent OTLP ingest corrupts traces.
  - Mitigation: Phase 0 includes an explicit "Go idiom checklist" task (errors wrapped with `%w`, no naked goroutines, `context.Context` first param) and a `go vet` + `golangci-lint` gate in CI from commit one.
  - Fallback: Keep the OTLP receiver single-threaded behind a buffered channel + one writer goroutine, eliminating concurrent-write hazards at a small throughput cost acceptable for local dev.

- **Risk: SQLite driver choice (cgo `mattn/go-sqlite3` vs pure-Go `modernc.org/sqlite`) creates toolchain friction.**
  - Severity: MEDIUM
  - Why it is risky: `mattn/go-sqlite3` needs a C toolchain and breaks cross-compilation / clean single-binary distribution; debugging cgo on a first Go project burns hours.
  - Mitigation: Lock `modernc.org/sqlite` (pure Go, no cgo) in Phase 0; verify a single static binary builds with `CGO_ENABLED=0 go build`.
  - Fallback: If pure-Go SQLite shows a performance issue at local trace volumes (unlikely under 10k spans/run), switch to cgo driver behind the same `database/sql` interface — zero query rewrites.

- **Risk: Bubble Tea TUI waterfall layout math (nested timeline bars at proportional widths) is fiddly and can stall the UI phase.**
  - Severity: MEDIUM
  - Why it is risky: Rendering a horizontal timeline where each span's bar offset and width are proportional to start/duration within a variable terminal width involves off-by-one and rounding bugs that look broken.
  - Mitigation: Build the proportional-bar math as a pure function (`layout.go`) unit-tested against fixture traces BEFORE wiring it into Bubble Tea; the static-waterfall printer in Phase 1 reuses the exact same layout function, so the math is proven before the interactive phase.
  - Fallback: Fall back to an indented tree view (depth = indentation, duration as a right-aligned number) if proportional bars prove unstable — still a legible waterfall, less visually rich.

### 1c. Shortest path to daily personal use

Ship Phase 0 + Phase 1 by end of week 2; this solves 60% of the user's pain — marks-based capture of his own build/test scripts plus a static waterfall printer already answers "where did the time go." Phase 3 (interactive TUI) ships by end of week 4 and adds the remaining 30% (history browsing, span drill-down). Phase 2 (OTLP receiver) is the portfolio/learning centerpiece and adds the final 10% of breadth — it makes Grotto a real local observability backend but is not required for daily personal use of marks-based profiling.

---

## Section 2: REVIEW GATE (SPEC LOCK)

### 2a. Goal

A single Go binary that captures OpenTelemetry spans from both `grotto mark` annotations and an OTLP endpoint, persists them to local SQLite, and renders queryable, comparable trace waterfalls in an interactive terminal UI.

### 2b. Success metrics

1. `grotto run -- ./fixture-build.sh` with 5 `grotto mark` calls produces a stored trace with exactly 6 spans (1 root + 5 marks) and correct parent nesting, verified by `grotto show <trace-id> --json`.
2. An external program exporting OTLP to `localhost:4318` lands a trace in SQLite with 100% of spans and attributes preserved, verified against a known 3-span fixture trace.
3. Interactive TUI renders a 200-span trace waterfall and responds to keyboard navigation (up/down/expand/collapse) in under 50 ms per keystroke on M4 Pro.
4. Single static binary builds with `CGO_ENABLED=0 go build` and is under 25 MB.
5. `grotto list` shows the last 50 runs with label, span count, and total duration; `grotto diff <id-a> <id-b>` reports per-span duration deltas.

### 2c. Hard constraints

1. **Local-first, zero telemetry out** — Grotto never makes an outbound network call; the OTLP receiver is inbound-only and binds to `127.0.0.1` by default.
2. **Genuine OpenTelemetry span model** — internal types mirror the OTel data model (trace_id, span_id, parent_span_id, kind, status, attributes); no homegrown timestamp tuples.
3. **No cgo** — pure-Go SQLite (`modernc.org/sqlite`); the deliverable is a single self-contained binary.
4. **Single binary, single config** — no daemon install required for marks mode; the OTLP receiver runs as a foreground `grotto serve` process.
5. **OTLP wire compatibility** — the receiver accepts standard OTLP/gRPC and OTLP/HTTP-protobuf without custom client changes.

### 2d. Locked decisions

- Decision: SQLite driver (cgo vs pure-Go).
  - Locked to: `modernc.org/sqlite` (pure Go, no cgo).
  - Rationale: Single static binary, clean cross-compile, no C toolchain friction on a first Go project; local trace volumes are far below where the cgo driver's speed edge matters.
  - Failure mode: If write throughput bottlenecks under heavy OTLP ingest, swap to `mattn/go-sqlite3` behind the same `database/sql` interface — no query changes.

- Decision: OTLP transport coverage in v1.
  - Locked to: Both gRPC (`:4317`) and HTTP/protobuf (`:4318`); gRPC is primary, HTTP is the fallback demo path.
  - Rationale: Hybrid B+C is the chosen scope; gRPC is the canonical OTLP transport and the deepest learning, HTTP is the simpler safety net if gRPC stalls.
  - Failure mode: If gRPC service registration blocks the phase, ship HTTP-only and defer gRPC to v1.1; the demo ("point an app at Grotto") still works over `:4318`.

- Decision: Marks transport (how `grotto mark` reaches the collector).
  - Locked to: `grotto run` starts an in-process span collector and exposes a unix-domain-socket + env var (`GROTTO_SOCK`) that child `grotto mark` invocations write to; spans assembled in the parent `run` process.
  - Rationale: Survives subprocess boundaries in shell scripts without a always-on daemon, and keeps marks-mode usable with zero setup.
  - Failure mode: If UDS plumbing proves flaky across shells, fall back to an append-only JSONL spool file at `$GROTTO_SOCK` path that `run` tails and assembles on exit.

---

## Section 3: ARCHITECTURE

### 3a. System diagram

```
  CAPTURE SOURCES                      SHARED SPINE                    PRESENTATION
  ───────────────                      ────────────                    ────────────

  [ ./script.sh ]                                                  [ grotto show <id> ]
  [ grotto mark  ] ──UDS/spool──┐                                    (static waterfall)
                                ▼                                          ▲
                        [ span ingest API ]                               │
  [ instrumented app ]          │            [ OTel span model ]──┐       │
  [ OTLP exporter    ] ──gRPC──►├──map proto─►(trace/span/attr) ──┼──►[ SQLite store ]
                       ──HTTP──►┘                                  │       │
                                                                   │       ▼
                                                              [ layout.go ]  [ grotto tui ]
                                                            (proportional   (Bubble Tea:
                                                             bar math)       list/waterfall/
                                                                             inspector)
```

### 3b. Tech stack

- **Go 1.22+** — single-binary infra language; the deliberate gap-fill skill being learned. Generics + `log/slog` available.
- **github.com/spf13/cobra v1.8+** — de-facto Go CLI framework (kubectl, gh use it); subcommand routing for `run`/`mark`/`serve`/`show`/`list`/`diff`/`tui`.
- **github.com/charmbracelet/bubbletea v0.27+** — the standard Go TUI framework (Elm-architecture); drives the interactive waterfall.
- **github.com/charmbracelet/lipgloss v0.13+** — styling/layout primitives for Bubble Tea (colors, borders, alignment).
- **github.com/charmbracelet/bubbles v0.20+** — prebuilt TUI components (viewport, list, key bindings).
- **go.opentelemetry.io/otel v1.30+ + otel/sdk v1.30+** — genuine OTel span data model + trace SDK for marks-mode span construction.
- **go.opentelemetry.io/proto/otlp v1.3+** — generated OTLP protobuf types for receiver decoding.
- **google.golang.org/grpc v1.66+** — gRPC server for the OTLP `:4317` endpoint.
- **google.golang.org/protobuf v1.34+** — protobuf runtime for OTLP/HTTP-protobuf decode.
- **modernc.org/sqlite v1.33+** — pure-Go SQLite (no cgo); single static binary.
- **github.com/stretchr/testify v1.9+** — assertion/require helpers for table-driven tests.
- **golangci-lint v1.60+** — aggregate linter (dev dependency, CI gate).

### 3c. File structure

```
Grotto/
├── cmd/
│   └── grotto/
│       └── main.go                 # entrypoint; wires cobra root
├── internal/
│   ├── cli/
│   │   ├── root.go                 # cobra root cmd + global flags
│   │   ├── run.go                  # `grotto run` — start collector, exec child
│   │   ├── mark.go                 # `grotto mark` — emit a span to GROTTO_SOCK
│   │   ├── serve.go                # `grotto serve` — start OTLP receiver
│   │   ├── show.go                 # `grotto show <id>` — static waterfall
│   │   ├── list.go                 # `grotto list` — recent runs
│   │   ├── diff.go                 # `grotto diff <a> <b>` — span deltas
│   │   └── tui.go                  # `grotto tui` — launch Bubble Tea
│   ├── model/
│   │   ├── span.go                 # OTel-shaped Span/Trace/Attribute structs
│   │   └── span_test.go            # span tree assembly unit tests
│   ├── collect/
│   │   ├── marks.go                # UDS/spool collector for grotto mark
│   │   └── marks_test.go
│   ├── otlp/
│   │   ├── grpc.go                 # OTLP gRPC trace service impl
│   │   ├── http.go                 # OTLP/HTTP-protobuf handler
│   │   ├── mapproto.go             # proto ResourceSpans → model.Span
│   │   └── mapproto_test.go        # proto→model mapping tests
│   ├── store/
│   │   ├── sqlite.go               # database/sql wrapper, migrations
│   │   ├── queries.go              # insert/list/get/diff queries
│   │   └── store_test.go           # round-trip persistence tests
│   ├── render/
│   │   ├── layout.go               # pure proportional-bar math
│   │   ├── layout_test.go          # layout math unit tests (fixture-driven)
│   │   └── waterfall.go            # static ANSI waterfall printer
│   └── tui/
│       ├── app.go                  # Bubble Tea root model
│       ├── runlist.go              # screen 1: run list
│       ├── waterfall.go            # screen 2: trace waterfall
│       └── inspector.go            # screen 3: span inspector
├── tests/
│   └── fixtures/
│       ├── three-span-trace.json   # known OTLP trace for receiver tests
│       ├── build-script.sh         # script with 5 grotto mark calls
│       └── expected-waterfall.txt  # golden static-waterfall output
├── specs/
│   ├── phase-0-validation.md
│   ├── phase-1-validation.md
│   ├── phase-2-validation.md
│   ├── phase-3-validation.md
│   └── phase-4-validation.md
├── migrations/
│   └── 001_init.sql                # schema (embedded via go:embed)
├── go.mod
├── go.sum
├── .golangci.yml
├── progress.json
├── tests.json
└── CLAUDE.md
```

### 3d. Data model

```sql
-- migrations/001_init.sql

CREATE TABLE traces (
    trace_id    TEXT PRIMARY KEY,           -- 16-byte hex (OTel trace id)
    run_label   TEXT NOT NULL,              -- user label or script name
    source      TEXT NOT NULL,              -- 'mark' | 'otlp'
    root_name   TEXT NOT NULL,              -- root span name
    started_ns  INTEGER NOT NULL,           -- unix nanos
    ended_ns    INTEGER NOT NULL,
    duration_ns INTEGER NOT NULL,
    span_count  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL            -- unix nanos of ingest
);

CREATE TABLE spans (
    span_id        TEXT PRIMARY KEY,        -- 8-byte hex (OTel span id)
    trace_id       TEXT NOT NULL REFERENCES traces(trace_id) ON DELETE CASCADE,
    parent_span_id TEXT,                    -- NULL for root
    name           TEXT NOT NULL,
    kind           INTEGER NOT NULL,        -- OTel SpanKind (0..5)
    status_code    INTEGER NOT NULL,        -- 0 unset, 1 ok, 2 error
    started_ns     INTEGER NOT NULL,
    ended_ns       INTEGER NOT NULL,
    duration_ns    INTEGER NOT NULL
);

CREATE TABLE span_attributes (
    span_id    TEXT NOT NULL REFERENCES spans(span_id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_type TEXT NOT NULL,               -- 'str'|'int'|'float'|'bool'
    value      TEXT NOT NULL,               -- stringified; typed on read
    PRIMARY KEY (span_id, key)
);

CREATE INDEX idx_spans_trace      ON spans(trace_id);
CREATE INDEX idx_spans_parent     ON spans(parent_span_id);
CREATE INDEX idx_traces_started   ON traces(started_ns DESC);
CREATE INDEX idx_attr_span        ON span_attributes(span_id);
```

### 3e. Type definitions

```go
// internal/model/span.go
package model

type SpanKind int32   // mirrors OTel: 0 Unspecified .. 5 Consumer
type StatusCode int32 // 0 Unset, 1 Ok, 2 Error

type Attribute struct {
    Key       string
    ValueType string // "str" | "int" | "float" | "bool"
    Value     string // stringified; callers type-assert on ValueType
}

type Span struct {
    SpanID       string // 8-byte hex
    TraceID      string // 16-byte hex
    ParentSpanID string // "" for root
    Name         string
    Kind         SpanKind
    Status       StatusCode
    StartedNs    int64
    EndedNs      int64
    DurationNs   int64
    Attributes   []Attribute
}

type Trace struct {
    TraceID    string
    RunLabel   string
    Source     string // "mark" | "otlp"
    RootName   string
    StartedNs  int64
    EndedNs    int64
    DurationNs int64
    SpanCount  int
    Spans      []Span // assembled tree (parent-ordered)
}

// TreeNode is the assembled hierarchy used by render + tui.
type TreeNode struct {
    Span     Span
    Depth    int
    Children []*TreeNode
}
```

### 3f. API contracts

Grotto makes **zero outbound external API calls** (hard constraint 1). The only network surface is the **inbound OTLP receiver**, documented as a protocol contract:

| Service | Endpoint | Method | Auth | Rate Limit | Pagination | Purpose |
|---------|----------|--------|------|------------|------------|---------|
| OTLP/gRPC | `127.0.0.1:4317` `opentelemetry.proto.collector.trace.v1.TraceService/Export` | gRPC unary | None (loopback-only bind) | None (local; backpressure via buffered channel, 1024 cap) | N/A (streamed `ResourceSpans`) | Ingest spans from any OTel-instrumented program |
| OTLP/HTTP | `127.0.0.1:4318/v1/traces` | POST | None (loopback-only bind) | None (local) | N/A (single protobuf body) | Ingest spans over HTTP/protobuf fallback path |

Internal request/response shapes (marks collector, store) are expressed as Go types in 3e; the marks collector consumes newline-delimited span records over `GROTTO_SOCK` and returns no response (fire-and-forget).

### 3g. Dependencies with install commands

```bash
# Runtime (added to go.mod via go get, version-pinned)
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

# Dev
go get github.com/stretchr/testify@v1.9.0

# System (Homebrew)
brew install go            # provides go 1.22+
brew install golangci-lint # v1.60+, CI lint gate
# optional, for receiver testing:
brew install otel-cli      # emit OTLP spans to localhost for Phase 2 verification
```

---

## Section 4: PHASED IMPLEMENTATION

## Phase 0: Toolchain + Scaffold + Span Model + Store (Week 1, first half)

### Agent Routing
- Recommended: Claude Code
- Rationale: Interactive scaffolding, first-Go-idiom learning, and schema design — all hands-on implementation.
- Note: Phase 0 is the framework context window — write `tests.json`, `progress.json`, and `migrations/001_init.sql` here; subsequent phases iterate from these artifacts rather than re-deriving project state.

### Objectives
- Verified Go toolchain, initialized module, Cobra root skeleton with stub subcommands.
- The shared OpenTelemetry span model (`internal/model`) and pure-Go SQLite store (`internal/store`) with round-trip persistence.
- Create `progress.json` at project root listing all phases and their tasks with status fields (`todo | in_progress | done`).
- Create `tests.json` at project root listing all planned test cases with their phase, type (unit | integration | e2e), and status (`todo | pass | fail`).

### Tasks
1. Verify toolchain and `go mod init github.com/saagar/grotto`; add `.golangci.yml`.
   - Context: Establishes the Go environment and lint gate before any code lands.
   - Acceptance: `go version` → go1.22+; `golangci-lint run ./...` → exits 0 on empty module.
2. Build Cobra root + 8 stub subcommands (`run/mark/serve/show/list/diff/tui`) that print "not implemented".
   - Context: Locks the command surface so later phases fill in bodies, not wiring.
   - Acceptance: `go run ./cmd/grotto --help` lists all 7 subcommands.
3. Implement `internal/model/span.go` (Span/Trace/Attribute/TreeNode) + tree-assembly function `AssembleTree(spans []Span) *TreeNode`.
   - Context: The shared spine both capture paths feed; tree assembly is reused by render + tui.
   - Acceptance: `go test ./internal/model/...` → tree test passes (6-span fixture → correct nesting/depths).
4. Implement `internal/store/sqlite.go` with `go:embed` migration + insert/get queries; `CGO_ENABLED=0` build.
   - Context: Persistence layer that both marks and OTLP write through.
   - Acceptance: `go test ./internal/store/...` → round-trip test stores + reloads a 6-span trace identically; `CGO_ENABLED=0 go build ./...` succeeds.
5. Write `progress.json` + `tests.json` at root enumerating all phases/tests.
   - Context: Session-resume state Claude Code reads first each session.
   - Acceptance: `cat progress.json` and `cat tests.json` → valid JSON; Phase 0 tasks marked done.

### Phase Verification Checklist
- [ ] `go version` → go1.22.x+
- [ ] `golangci-lint run ./...` → exits 0
- [ ] `go test ./internal/model/... ./internal/store/...` → all pass
- [ ] `CGO_ENABLED=0 go build ./cmd/grotto` → produces a binary
- [ ] `cat progress.json` → valid JSON with all Phase 0 tasks marked "done"
- [ ] `cat tests.json` → valid JSON listing planned test cases for all phases

### Risks & Mitigations
- Risk: cgo accidentally pulled in via a transitive dep, breaking static build.
  - Mitigation: CI runs `CGO_ENABLED=0 go build` as a gate from Phase 0.
  - Fallback: `go mod why` the offender; replace with a pure-Go alternative.
- Risk: Go-idiom drift (Python/Rust habits) seeds rework.
  - Mitigation: `.golangci.yml` enables `errcheck`, `govet`, `staticcheck`; errors wrapped with `%w`.
  - Fallback: A short Go-idiom review pass at phase end against the checklist in CLAUDE.md.

### Parallel Dispatch Proposal
- Dispatchable in parallel: Task 3 (model), Task 4 (store) — after Task 1–2 scaffold lands.
- Subagent type: coder (Sonnet)
- Rationale: `internal/model` and `internal/store` share only the Span struct (defined first in Task 3); store imports model but the two test suites are disjoint and can be built concurrently once the struct signature is fixed.

### Phase Validation Artifact
- File: `specs/phase-0-validation.md`
- Contents: the Phase Verification Checklist above as pass/fail conditions.
- Written by this skill as part of plan generation; read by Claude Code at phase completion.

## Phase 1: Marks Capture → Store → Static Waterfall (Week 1, second half → Week 2)

### Agent Routing
- Recommended: Claude Code
- Rationale: Interactive subprocess/IPC plumbing + the first end-to-end vertical slice; hands-on debugging.

### Objectives
- `grotto run -- <cmd>` starts an in-process collector exposing `GROTTO_SOCK`; child `grotto mark "step"` calls emit spans assembled into one trace on exit.
- `internal/render/layout.go` proportional-bar math (pure, tested) + `grotto show <id>` static ANSI waterfall printer.
- First demoable, shippable checkpoint: profile a real script and see the waterfall.

### Tasks
1. Implement `internal/collect/marks.go` — `run` opens a unix-domain socket at `GROTTO_SOCK`, execs the child with the env var set, collects span records, assembles a `Trace`.
   - Context: Realizes capture model B over subprocess boundaries without a daemon.
   - Acceptance: `grotto run -- tests/fixtures/build-script.sh` stores a trace; `grotto show <id> --json` shows 6 spans with correct parents.
2. Implement `grotto mark` — connects to `GROTTO_SOCK`, writes a span record (name, start, end inferred from prior mark), exits.
   - Context: The user-facing annotation primitive; pairs with Task 1's collector.
   - Acceptance: Running `build-script.sh` (5 marks) yields exactly 5 child spans under 1 root.
3. Implement `internal/render/layout.go` — pure function mapping a `TreeNode` + terminal width → per-span bar offset/width; unit-test against fixtures.
   - Context: Layout math reused later by the interactive TUI — proving it here de-risks Phase 3.
   - Acceptance: `go test ./internal/render/...` → layout test matches expected offsets within ±1 char for a 6-span fixture.
4. Implement `internal/render/waterfall.go` + wire `grotto show <id>` to print the static ANSI waterfall.
   - Context: First visible product output; golden-file tested.
   - Acceptance: `grotto show <id>` output matches `tests/fixtures/expected-waterfall.txt`.

### Phase Verification Checklist
- [ ] `grotto run -- tests/fixtures/build-script.sh` → "stored trace <id>"
- [ ] `grotto show <id> --json` → 6 spans, correct parent_span_id nesting
- [ ] `go test ./internal/render/... ./internal/collect/...` → all pass
- [ ] `grotto show <id>` → renders a nested waterfall with proportional bars

### Risks & Mitigations
- Risk: UDS span plumbing flaky across shells (zsh/bash subshell env inheritance).
  - Mitigation: Integration test runs the fixture script under `sh -c`; assert span count.
  - Fallback: JSONL spool-file mode (locked fallback in 2d) tailed by `run` on exit.
- Risk: Mark timing ambiguity (a mark ends the prior mark vs nests under it).
  - Mitigation: Define explicit semantics — sibling marks by default, `--child` flag to nest; documented in CLAUDE.md.
  - Fallback: Sibling-only in v1; nesting deferred.

### Parallel Dispatch Proposal
- Dispatchable in parallel: Task 1 (collector) and Task 3 (layout math) — no shared code; layout consumes the model struct only.
- Subagent type: coder (Sonnet)
- Rationale: `internal/collect` and `internal/render/layout` have no compile dependency on each other; both depend only on `internal/model` from Phase 0.

### Phase Validation Artifact
- File: `specs/phase-1-validation.md`
- Contents: Phase 1 verification checklist as pass/fail conditions.

## Phase 2: OTLP Receiver (gRPC + HTTP) (Week 3)

### Agent Routing
- Recommended: Claude Code
- Rationale: The hardest Go lift (gRPC service impl + protobuf mapping); needs interactive debugging against a live exporter.

### Objectives
- `grotto serve` runs an OTLP receiver: gRPC on `:4317` and HTTP/protobuf on `:4318`, both loopback-bound.
- `internal/otlp/mapproto.go` maps protobuf `ResourceSpans` → `model.Span`, preserving nesting + attributes, into the same store.
- Shippable checkpoint: point any OTel-instrumented program at Grotto and see its trace.

### Tasks
1. Implement `internal/otlp/grpc.go` — register the generated `TraceService` server, decode `ExportTraceServiceRequest`.
   - Context: The canonical OTLP transport; deepest OTel learning.
   - Acceptance: `otel-cli span --endpoint localhost:4317 ...` lands a trace; `grotto list` shows it with source=otlp.
2. Implement `internal/otlp/mapproto.go` — protobuf spans → `model.Span` (hex IDs, kind, status, attributes by type).
   - Context: Correct mapping is what makes ingested traces render identically to marks traces.
   - Acceptance: `go test ./internal/otlp/...` → maps `tests/fixtures/three-span-trace.json` to 3 spans, 1 attribute preserved per span, parents intact.
3. Implement `internal/otlp/http.go` — POST `/v1/traces` protobuf handler sharing the same map+store path.
   - Context: Simpler fallback transport; satisfies the demo if gRPC stalls.
   - Acceptance: `curl --data-binary @fixture.pb localhost:4318/v1/traces` → trace stored.
4. Add a buffered-channel + single-writer goroutine between receiver and store.
   - Context: Eliminates concurrent-write hazards (MEDIUM risk in 1b) under multi-span ingest.
   - Acceptance: A 200-span export stores with zero `database is locked` errors across 10 runs.

### Phase Verification Checklist
- [ ] `grotto serve` → binds `:4317` and `:4318` on 127.0.0.1
- [ ] `otel-cli span --endpoint localhost:4317 --name test` → `grotto list` shows it
- [ ] `go test ./internal/otlp/...` → mapping tests pass (span count, parents, attributes)
- [ ] 200-span ingest → stored trace has 200 spans, no lock errors

### Risks & Mitigations
- Risk: gRPC service registration / proto version mismatch blocks the phase.
  - Mitigation: Pin `go.opentelemetry.io/proto/otlp` + `grpc` to the versions in 3g; validate against `otel-cli` early.
  - Fallback: Ship HTTP-only receiver, defer gRPC to v1.1 (locked fallback in 2d).
- Risk: Attribute type fidelity lost (everything stored as string).
  - Mitigation: `value_type` column + typed round-trip test asserts int stays int.
  - Fallback: Store raw protobuf JSON alongside for lossless re-read.

### Parallel Dispatch Proposal
- Dispatchable in parallel: Task 1 (gRPC server) and Task 3 (HTTP handler) — both call the shared `mapproto` (Task 2) but neither imports the other.
- Subagent type: coder (Sonnet)
- Rationale: After Task 2's mapping function exists, the two transport handlers are independent surfaces over the same map+store path.

### Phase Validation Artifact
- File: `specs/phase-2-validation.md`
- Contents: Phase 2 verification checklist as pass/fail conditions.

## Phase 3: Interactive Bubble Tea TUI (Week 4)

### Agent Routing
- Recommended: Claude Code
- Rationale: Interactive UI build with tight visual-feedback loops; Bubble Tea state machine debugging.

### Objectives
- `grotto tui` launches a 3-screen Bubble Tea app: **Run List** (recent traces), **Waterfall** (selected trace, proportional bars reusing `layout.go`), **Span Inspector** (attributes/timing for a focused span).
- Keyboard navigation: up/down, enter to drill in, esc to go back, expand/collapse subtrees.

### Tasks
1. Implement `internal/tui/app.go` — Bubble Tea root model with screen-state enum + key routing.
   - Context: The Elm-architecture spine the three screens plug into.
   - Acceptance: `grotto tui` launches, `q` quits cleanly, screen transitions work.
2. Implement `internal/tui/runlist.go` (Screen 1) — list last 50 traces via `store` query; enter selects.
   - Context: Entry screen reusing the Phase 0 store list query.
   - Acceptance: List shows label/span-count/duration; enter opens the waterfall.
3. Implement `internal/tui/waterfall.go` (Screen 2) — render the proportional waterfall from `layout.go`, scroll + expand/collapse.
   - Context: The product centerpiece; reuses the layout math proven in Phase 1.
   - Acceptance: 200-span trace navigates at <50 ms/keystroke; collapse hides subtrees.
4. Implement `internal/tui/inspector.go` (Screen 3) — focused span's attributes, kind, status, exact timing.
   - Context: Drill-down detail closing the loop from overview to specifics.
   - Acceptance: Enter on a span shows all its attributes typed correctly; esc returns to waterfall.

### Phase Verification Checklist
- [ ] `grotto tui` → run list renders, navigable
- [ ] enter on a run → waterfall screen with proportional bars
- [ ] expand/collapse + scroll respond at <50 ms/keystroke on a 200-span trace
- [ ] enter on a span → inspector shows typed attributes; esc returns

### Risks & Mitigations
- Risk: Waterfall layout bugs (off-by-one bar widths) under variable terminal width.
  - Mitigation: `layout.go` already unit-tested in Phase 1; TUI only consumes it.
  - Fallback: Indented-tree view fallback (locked fallback in 1b).
- Risk: Bubble Tea perf on large traces (full re-render per keystroke).
  - Mitigation: Use `bubbles/viewport` for windowed rendering; only visible rows drawn.
  - Fallback: Cap rendered depth + lazy-expand subtrees on demand.

### Parallel Dispatch Proposal
- Dispatchable in parallel: Task 2 (runlist), Task 4 (inspector) — independent screens once Task 1's root model + screen enum exist.
- Subagent type: coder (Sonnet)
- Rationale: Run-list and inspector screens share no state beyond the root model's selection; waterfall (Task 3) is the integration point and stays sequential after Task 1.

### Phase Validation Artifact
- File: `specs/phase-3-validation.md`
- Contents: Phase 3 verification checklist as pass/fail conditions.

## Phase 4: Compare, Polish, Distribution (Week 5)

### Agent Routing
- Recommended: Claude Code
- Rationale: Feature completion (diff), packaging, and OTel-fidelity polish; interactive.

### Objectives
- `grotto list` (recent runs table) + `grotto diff <a> <b>` (per-span duration deltas across two runs).
- Distribution: `CGO_ENABLED=0` cross-compiled static binaries + `README.md` with the OTel/Go learning writeup (the career artifact).
- OTel fidelity polish: span kind/status surfaced in TUI + static output.

### Tasks
1. Implement `grotto diff <a> <b>` — match spans by name+depth, report duration deltas.
   - Context: Turns Grotto from a viewer into a regression tool ("this build got 2s slower").
   - Acceptance: `grotto diff <id-a> <id-b>` on two fixture runs prints +/- ms per matched span.
2. Implement `grotto list` table (label, span count, duration, source, age).
   - Context: The history index that makes SQLite persistence worthwhile.
   - Acceptance: `grotto list` shows the last 50 runs newest-first.
3. Build release artifacts — `make build` cross-compiles darwin/arm64 + linux/amd64 static binaries.
   - Context: Single-binary distribution is a hard constraint and a portfolio signal.
   - Acceptance: `file dist/grotto-darwin-arm64` → Mach-O 64-bit executable, <25 MB.
4. Write `README.md` — what/why + an explicit "OTel data model + Go idioms learned" section.
   - Context: The career-facing artifact; makes the gap-fill legible to a hiring reader.
   - Acceptance: README documents the marks-vs-OTLP architecture + the OTel span model with a diagram.

### Phase Verification Checklist
- [ ] `grotto diff <a> <b>` → per-span deltas printed
- [ ] `grotto list` → last 50 runs, newest first
- [ ] `make build` → static binaries for 2 targets, each <25 MB
- [ ] `README.md` → contains architecture diagram + OTel/Go learning section

### Risks & Mitigations
- Risk: Span matching for diff is ambiguous when names repeat.
  - Mitigation: Match by (name, depth, sibling-index) tuple; document the heuristic.
  - Fallback: Match by name only with a "multiple matches" warning.
- Risk: Cross-compile surfaces a hidden cgo dependency.
  - Mitigation: `CGO_ENABLED=0` gate has run since Phase 0; `go mod why` any offender.
  - Fallback: Ship darwin/arm64 only for v1.

### Parallel Dispatch Proposal
- Dispatchable in parallel: Task 1 (diff), Task 2 (list), Task 4 (README) — disjoint surfaces.
- Subagent type: coder (Sonnet) for Task 1–2; research (Haiku) for Task 4 draft.
- Rationale: diff and list are independent query commands; README is documentation with no code dependency.

### Phase Validation Artifact
- File: `specs/phase-4-validation.md`
- Contents: Phase 4 verification checklist as pass/fail conditions.

---

## Section 5: SECURITY AND CREDENTIALS

- **Credential storage:** No credentials in scope — Grotto is local-only and authenticates nothing. The OTLP receiver binds to `127.0.0.1` and accepts unauthenticated spans by design (local trust boundary).
- **Data boundaries:** Nothing leaves the machine. Hard constraint 1 forbids outbound calls; the receiver is inbound-loopback-only. Trace data (which may include command names, file paths, and user-set attributes) persists only to local SQLite at `~/.grotto/grotto.db`.
- **Encryption at rest:** None in v1 — local SQLite on an already-FileVault-encrypted M4 Pro; adding app-level encryption would block the single-binary/no-config constraint for no threat-model gain. Documented as a deliberate v1 decision.
- **Token rotation:** Not applicable — no auth tokens anywhere in the system.
- **Sensitive data handling:** Span attributes and command strings may incidentally contain secrets (e.g., a command with an inline token). Grotto applies a redaction pass on ingest: attribute values and span names matching common secret patterns (`AKIA[0-9A-Z]{16}`, `ghp_[0-9A-Za-z]{36}`, `sk-[0-9A-Za-z]{20,}`, `xox[baprs]-`) are replaced with `‹redacted›` before persistence. Redaction patterns live in `internal/store/redact.go` and are unit-tested against a fixture of known secret shapes.

---

## Section 6: TESTING STRATEGY

**Phase 0**
- Manual: `grotto --help` lists subcommands; `CGO_ENABLED=0 go build` produces a binary.
- Automate: unit tests for `AssembleTree` (model) and store round-trip (`internal/store/store_test.go`).
- Verify correctness: fixture `tests/fixtures/three-span-trace.json` (1 root + 2 children) → assembled tree has depths [0,1,1]; store reload returns identical spans.

**Phase 1**
- Manual: run `tests/fixtures/build-script.sh` under `grotto run`; eyeball the static waterfall.
- Automate: unit-test `layout.go` bar math against fixtures; integration-test marks collector span count.
- Verify correctness: `tests/fixtures/expected-waterfall.txt` golden file (6-span build) — `grotto show` output must match byte-for-byte; `build-script.sh` must yield exactly 5 child marks.

**Phase 2**
- Manual: `grotto serve`, then `otel-cli span --endpoint localhost:4317`; confirm `grotto list` shows source=otlp.
- Automate: `mapproto_test.go` proto→model mapping; concurrent-ingest lock test (200 spans × 10 runs).
- Verify correctness: `tests/fixtures/three-span-trace.json` ingested over both `:4317` and `:4318` → identical stored trees; one int + one string attribute preserved with types.

**Phase 3**
- Manual: launch `grotto tui`, navigate run-list → waterfall → inspector; verify expand/collapse and <50 ms feel.
- Automate: Bubble Tea model unit tests via `teatest` — assert key messages drive correct screen transitions; layout reuse already covered by Phase 1 tests.
- Verify correctness: seed the store with a 200-span fixture trace; `teatest` asserts down-arrow moves selection and enter pushes the waterfall screen.

**Phase 4**
- Manual: `grotto diff` two runs of the same script (one artificially slowed) and confirm the slowed span shows a positive delta; `grotto list` ordering.
- Automate: `diff` unit test on two fixture traces with a known per-span delta; redaction unit tests on a secrets fixture.
- Verify correctness: `tests/fixtures/three-span-trace.json` + a `three-span-trace-slow.json` variant → diff reports the exact ns delta on the matched span; redaction fixture (`tests/fixtures/secrets-attrs.json`) → all 4 secret patterns become `‹redacted›`.
