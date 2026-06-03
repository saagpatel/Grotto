# Grotto

**See exactly where time goes in a slow build, test suite, or shell script — without sending a byte to the cloud.**

Grotto is a local-first Go CLI and Bubble Tea TUI that captures OpenTelemetry traces from shell commands and OTLP-instrumented applications and renders them as proportional-bar waterfalls. Every trace is stored in a local SQLite database (`~/.grotto/grotto.db`). Nothing leaves the machine.

---

## Why it exists

This project is a deliberate skill-fill: Go and observability are the two gaps in a Platform Engineer (DX & AI Infrastructure) profile that had been iOS/Rust/TypeScript-heavy. Building Grotto meant working through genuine OpenTelemetry data model concepts — trace IDs, span IDs, parent chains, span kind, status, typed attributes — rather than a simplified homegrown timestamp format. The OTLP protobuf-to-internal-model mapping, the single-writer SQLite concurrency constraint, and the Bubble Tea TUI architecture are all load-bearing learning exercises, not shortcuts around them.

---

## Architecture

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

The load-bearing design decision: **both capture paths converge on one span model, one store, and one rendering layer.** `grotto mark` emits spans over a Unix domain socket (`GROTTO_SOCK`) with a JSONL spool file as a fallback when the socket is unreachable. `grotto serve` runs a loopback OTLP receiver that accepts gRPC (`:4317`) and HTTP (`:4318`) and maps protobuf `ResourceSpans` into the same `model.Span` structs. `internal/render/layout.go` — a pure function from a span tree and terminal width to per-span bar offset/width — is shared by both `grotto show` (static printer) and the interactive TUI.

### Key decisions

| Decision | Choice | Why |
|---|---|---|
| SQLite driver | `modernc.org/sqlite` (pure Go) | No cgo; single static binary; local volumes don't need the cgo speed edge |
| OTLP transport | gRPC `:4317` + HTTP `:4318`, gRPC primary | gRPC is the canonical OTLP transport and the deeper learning; HTTP is the fallback demo path |
| Span capture | Hybrid: `grotto mark` AND OTLP receiver | Both feed one OTel span model → one SQLite store → one TUI |
| Internal data model | Genuine OTel spans (trace/span/parent/kind/status/attrs) | The OTel data-model learning is half the point — no homegrown timestamps |
| Marks transport | UDS at `GROTTO_SOCK`, JSONL spool fallback | Survives subprocess boundaries without an always-on daemon |

---

## Build

Grotto requires Go 1.22+ and has no cgo dependency. The SQLite driver is `modernc.org/sqlite` (pure Go), so the binary is fully static.

```bash
# Single binary for your current platform
CGO_ENABLED=0 go build -o grotto ./cmd/grotto

# Lint (golangci-lint 1.60+)
golangci-lint run ./...

# Tests
go test ./...
```

Cross-compiled static binaries (`dist/grotto-darwin-arm64`, `dist/grotto-linux-amd64`, each ~17 MB) are produced via `make build` / `make build-all`. The `CGO_ENABLED=0` gate is enforced from the first commit — any transitive cgo dependency is a hard build failure, not a warning.

---

## Usage

### Capture marks from a shell script

Wrap any command with `grotto run`. Any `grotto mark` calls inside the command (or its subprocesses) are collected over the Unix domain socket and assembled into one trace.

```bash
# Run a script and capture its marks as OTel spans
grotto run -- ./build.sh

# From inside the script, emit a span boundary
grotto mark "compile"
grotto mark "link"
grotto mark "test"
```

`grotto run` sets `GROTTO_SOCK` and `GROTTO_SPOOL` in the child's environment. Each `grotto mark <name>` call opens the socket, writes a JSON record, and waits for a one-byte acknowledgement — so the mark is durably held before the `grotto mark` process exits. If the socket is unreachable (e.g. nested subshell), the mark spools to `GROTTO_SPOOL` instead. N marks produce N child spans under one root span covering the full command duration.

### Receive OTLP spans from an instrumented app

```bash
# Start the loopback receiver (gRPC :4317 + HTTP :4318)
grotto serve

# In another terminal, point any OTel exporter at localhost:4317
# or use otel-cli to emit a test span
otel-cli span --endpoint localhost:4317 --name "my-span"
```

The receiver binds `127.0.0.1` only. It warns on stderr if a non-loopback address is configured.

### Inspect traces

```bash
# List recent traces (newest first, last 50)
grotto list

# Static waterfall for a trace
grotto show <trace-id>

# Raw JSON (spans + attributes)
grotto show <trace-id> --json

# Per-span duration delta between two runs
grotto diff <trace-id-a> <trace-id-b>

# Interactive TUI: run list → waterfall → span inspector
grotto tui
```

### TUI navigation

`grotto tui` opens a three-screen Bubble Tea app. **Screen 1** (Run List) shows recent traces with label, span count, duration, and source (`mark` or `otlp`). Press `enter` to open the **Waterfall** view (Screen 2), which renders proportional bars with keyboard scroll and expand/collapse. Press `enter` on any span to open the **Inspector** (Screen 3), which shows the span's typed attributes, kind, status, and timing. `esc` returns up a level; `q` quits.

---

## What I learned: the OpenTelemetry data model + Go idioms

### Genuine OTel shapes instead of homegrown timestamps

The temptation on a project like this is to model "spans" as a simple pair of timestamps with a name. Grotto deliberately resists that — every span carries the full OTel shape: a 128-bit trace ID and 64-bit span ID in hex, a nullable `parent_span_id` (empty string for roots), a `SpanKind` (Internal/Server/Client/Producer/Consumer), a `StatusCode` (Unset/Ok/Error), nanosecond start/end times, and typed attributes (`str`, `int`, `float`, `bool`). This matters because the OTLP receiver maps real protobuf `ResourceSpans` into these types — if the internal model were simpler, something would have to be thrown away, and the mapping would be lying about what OTel actually carries.

### Assembling a tree from a flat span list

The SQLite store persists spans in a flat table with a `parent_span_id` foreign key. `AssembleTree` in `internal/model/span.go` reconstructs the parent/child hierarchy from that flat list: it builds a map from span ID to `*TreeNode`, iterates to wire children under parents, and then sorts each node's children by start time for deterministic rendering. The function is defensive against malformed input from both capture paths — duplicate span IDs (first wins), multiple root spans (first in input order wins), and orphaned or self-parented spans (dropped, never attached, so no cycle is reachable from the root). That defensive stance came from a real constraint: the OTLP receiver accepts spans from any exporter, not just ones Grotto emitted.

### The OTLP protobuf → internal model mapping

`internal/otlp/mapproto.go` maps `otlp.proto`'s `ResourceSpans` → `ScopeSpans` → `Span` chain into `model.Span`. The interesting parts: span and trace IDs in the proto are raw `[]byte` and need to be hex-encoded to match what the marks path generates; attribute values are a protobuf oneof (`AnyValue`) that has to be type-switched to recover `str`/`int`/`float`/`bool` and store the type tag alongside the string representation; and `SpanKind` and `StatusCode` are proto enums that map 1:1 to the internal constants. Getting the attribute round-trip right — so `grotto show --json` emits the same typed value the exporter sent — required unit-testing the mapping against a fixture rather than trusting the obvious-looking code.

### Pure-Go SQLite as a hard constraint

`modernc.org/sqlite` is a pure-Go reimplementation of SQLite via generated C-to-Go translation. The reason it matters here is not performance — a local trace store with hundreds of spans doesn't need `mattn/go-sqlite3`'s speed. It matters because `CGO_ENABLED=0 go build` is a hard gate from Phase 0. A cgo dependency anywhere in the dependency graph breaks the single-static-binary constraint that makes Grotto distributable as a `curl | tar` install. `go mod why` catches offenders; replacing them is mandatory, not optional.

### Single-writer SQLite to avoid lock contention

The OTLP receiver runs two concurrent goroutines (gRPC server + HTTP handler). Both need to write spans to the same SQLite file. SQLite supports only one writer at a time, so naively opening the db from both would produce `database is locked` errors under load. The solution in `internal/store/sqlite.go` is `db.SetMaxOpenConns(1)` — the connection pool is capped to one, so the `database/sql` pool serializes all writes. The OTLP `Sink` in `internal/otlp/sink.go` adds a buffered channel in front of the store: receivers write to the channel, a single goroutine drains it to the store. This gives the gRPC and HTTP handlers non-blocking ingest while keeping the store single-writer.

### Go error wrapping, context, and goroutine ownership

Coming from Rust (explicit `Result`) and Python (exceptions), Go's error model needed deliberate attention. The rule enforced throughout: every error that crosses a function boundary is wrapped with `%w` so callers can use `errors.Is`/`errors.As`. Errors are never silently swallowed — either returned or logged. Every blocking or IO function takes `context.Context` as its first parameter, and cancellation propagates through the exec'd child command (`exec.CommandContext`), the gRPC server, and the HTTP server shutdown. Every `go` statement has a named owner and a clear exit path — no fire-and-forget goroutines. The collector's concurrent mark handlers, the sink's drain goroutine, and the two OTLP server goroutines each have explicit `WaitGroup` or channel coordination to ensure clean shutdown.

---

## Security / privacy

Grotto is local-only by design. The OTLP receiver binds `127.0.0.1` and is unauthenticated — this is deliberate for a developer tool, and Grotto warns on stderr if a non-loopback address is used. No trace data leaves the machine; the database lives at `~/.grotto/grotto.db` (overridable via `GROTTO_DB`).

Before any trace is written to disk, `internal/store/redact.go` applies a redaction pass against four credential patterns: AWS access key IDs (`AKIA…`), GitHub personal access tokens (`ghp_…`), OpenAI-style secret keys (`sk-…`), and Slack tokens (`xox[baprs]-…`). Matches in span names, run labels, and attribute values are replaced with `‹redacted›`. The redaction runs at the single `InsertTrace` chokepoint so both capture paths are covered without duplicating the logic.

---

## Stack

- **Go** 1.22+
- **CLI** — [Cobra](https://github.com/spf13/cobra) 1.8+
- **TUI** — [Bubble Tea](https://github.com/charmbracelet/bubbletea) 0.27+ / lipgloss / bubbles
- **Tracing** — OpenTelemetry Go SDK 1.30+ · OTLP proto 1.3+ · gRPC 1.66+
- **Storage** — [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) 1.33+ (pure Go, no cgo)
