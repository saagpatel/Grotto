# Grotto — single static binary, no cgo. The pure-Go SQLite driver lets every
# target build with CGO_ENABLED=0, so cross-compilation needs no C toolchain.
BINARY  := grotto
PKG     := ./cmd/grotto
DIST    := dist
# VERSION is derived from git tags (e.g. v1.2.0, or v1.1.1-2-gabc123-dirty mid-work);
# plain `go build` without this ldflag falls back to the "dev" default.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# -s -w strip the symbol table and DWARF info, keeping the binary well under the
# 25 MB target; -X injects the build version into the cli package for `--version`.
LDFLAGS := -s -w -X github.com/saagpatel/grotto/internal/cli.version=$(VERSION)

.PHONY: build build-all test lint clean

build: ## Build a static binary for the host platform
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

build-all: $(DIST)/$(BINARY)-darwin-arm64 $(DIST)/$(BINARY)-linux-amd64 ## Cross-compile release binaries

$(DIST)/$(BINARY)-darwin-arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $@ $(PKG)

$(DIST)/$(BINARY)-linux-amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $@ $(PKG)

test: ## Run the full test suite
	go test ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

clean: ## Remove build artifacts
	rm -rf $(DIST) $(BINARY)
