# Phase 0 Validation — Toolchain + Scaffold + Span Model + Store

Pass/fail conditions. Read at phase completion, not during implementation.

- [ ] PASS iff `go version` reports go1.22.x or higher.
- [ ] PASS iff `golangci-lint run ./...` exits 0.
- [ ] PASS iff `go test ./internal/model/... ./internal/store/...` reports all tests passing.
- [ ] PASS iff `CGO_ENABLED=0 go build ./cmd/grotto` produces a binary with no cgo error.
- [ ] PASS iff `cat progress.json` is valid JSON with every Phase 0 task status = "done".
- [ ] PASS iff `cat tests.json` is valid JSON listing planned test cases for all phases.
- [ ] PASS iff `go run ./cmd/grotto --help` lists all 7 subcommands (run, mark, serve, show, list, diff, tui).

FAIL on any unchecked box → fix before advancing to Phase 1.
