# Grotto

## Overview
Local-first Go CLI + TUI that renders OpenTelemetry trace waterfalls for shell commands, build scripts, and test suites — see exactly where time goes in a slow run. Zero cloud backend; everything persists to local SQLite. Built by the operator as a deliberate Go + observability gap-fill for a Platform Engineer — DX & AI Infrastructure target.

## Tech Stack
- Go: 1.22+ (first Go project — idiom guardrails below are load-bearing)
- CLI: Cobra 1.8+
- TUI: Bubble Tea 0.27+ / lipgloss / bubbles
- Tracing: OpenTelemetry Go SDK 1.30+ + OTLP proto 1.3+ + gRPC 1.66+
- Storage: modernc.org/sqlite 1.33+ (pure Go, NO cgo)

## Development Conventions
- Go idioms (enforced, not optional): wrap errors with `%w`; no naked `go` statements (every goroutine has a clear owner + exit); `context.Context` is the first param of any blocking/IO function; no `panic` in library code.
- Build gate from commit one: `CGO_ENABLED=0 go build ./...` must succeed (single static binary is a hard constraint).
- Lint gate: `golangci-lint run ./...` clean before every commit (errcheck, govet, staticcheck on).
- Filenames lower_snake or single-word; packages lowercase; exported types documented.
- Conventional commits: feat:, fix:, chore:, docs:. Small logical units.

## CC Infrastructure
This project inherits the global CC setup: 34+ skills, agents, hooks, and MCP plugins.
Project-specific overrides only — see IMPLEMENTATION-ROADMAP.md for architecture.

## Current Phase
**v1 complete; public release current through v1.8.3.**
All 7 subcommands live (run/mark/serve/show/list/diff/tui), OTLP receiver, interactive TUI,
secret redaction on ingest, cross-compiled static binaries, README, and three adapters:
`cargo`, `go-test`, `junit`. See IMPLEMENTATION-ROADMAP.md and HANDOFF.md.

**Active additive increment:** P08 adds an eighth source-level command,
`redact-preview`, plus a versioned canonical ingest/preview evaluator. This is
branch state until merged and is not part of the v1.8.3 public binary.

## Key Decisions
| Decision | Choice | Why |
|----------|--------|-----|
| SQLite driver | `modernc.org/sqlite` (pure Go) | No cgo; single static binary; local volumes don't need the cgo speed edge |
| OTLP transport | gRPC `:4317` + HTTP `:4318`, gRPC primary | Hybrid B+C scope; gRPC is canonical + deepest learning, HTTP is the fallback demo path |
| Span capture | Hybrid: `grotto mark` AND OTLP receiver | Both feed ONE OTel span model → ONE SQLite store → ONE TUI |
| Internal data model | Genuine OTel spans (trace/span/parent/kind/status/attrs) | The OTel data-model learning is half the point — no homegrown timestamps |
| Marks transport | UDS at `GROTTO_SOCK`, JSONL spool fallback | Survives subprocess boundaries without an always-on daemon |

## Phase-Boundary Review
At the end of every phase, run `/ultrareview` before committing the phase-final code. Do not skip on phases that feel small.

## Do NOT
- Do not add features not in the current phase of IMPLEMENTATION-ROADMAP.md.
- Do not introduce a cgo dependency — `CGO_ENABLED=0 go build` is a hard gate; `go mod why` any offender and replace with pure Go.
- Do not invent a homegrown span/timestamp shape — model everything as genuine OpenTelemetry spans.
- Do not start the OTLP receiver (Phase 2) before the marks→store→waterfall slice (Phases 0–1) works end-to-end.

<!-- portfolio-context:start -->
# Portfolio Context

## What This Project Is

Local-first Go CLI + TUI that renders OpenTelemetry trace waterfalls for shell commands, build scripts, and test suites — see exactly where time goes in a slow run. Zero cloud backend; everything persists to local SQLite. Built by the operator as a deliberate Go + observability gap-fill for a Platform Engineer — DX & AI Infrastructure target.

## Current State

**v1 complete; public release current through v1.8.3.**
All 7 subcommands live (run/mark/serve/show/list/diff/tui), OTLP receiver, interactive TUI,
secret redaction on ingest, cross-compiled static binaries, README, and three adapters:
`cargo`, `go-test`, `junit`. See IMPLEMENTATION-ROADMAP.md and HANDOFF.md.

P08 Trace Redaction Preview is the only reactivated post-v1 increment in this
worktree. Its source-level command is not a claim about the v1.8.3 release.

## Stack

- Go: 1.22+ (first Go project — idiom guardrails below are load-bearing)
- CLI: Cobra 1.8+
- TUI: Bubble Tea 0.27+ / lipgloss / bubbles
- Tracing: OpenTelemetry Go SDK 1.30+ + OTLP proto 1.3+ + gRPC 1.66+
- Storage: modernc.org/sqlite 1.33+ (pure Go, NO cgo)

## How To Run

- Build/test: `CGO_ENABLED=0 go build ./...`, `go test ./...`, `golangci-lint run ./...`.
- Demo latest release: download `v1.8.3`, verify `grotto v1.8.3`, then run
  `grotto run --adapter=junit -- python3 -m pytest` or import an existing report
  with `grotto run --adapter=junit --junit-file=reports/junit.xml -- true`.

## Known Risks

- Do not add features not in the current phase of IMPLEMENTATION-ROADMAP.md.
- Do not introduce a cgo dependency — `CGO_ENABLED=0 go build` is a hard gate; `go mod why` any offender and replace with pure Go.
- Do not invent a homegrown span/timestamp shape — model everything as genuine OpenTelemetry spans.
- Do not start the OTLP receiver (Phase 2) before the marks→store→waterfall slice (Phases 0–1) works end-to-end.

## Next Recommended Move

Use this context plus the README and supporting docs to resume the next active task, then promote the repo beyond minimum-viable by capturing a dedicated handoff, roadmap, or discovery artifact.

<!-- portfolio-context:end -->
