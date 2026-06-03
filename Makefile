# Grotto — single static binary, no cgo. The pure-Go SQLite driver lets every
# target build with CGO_ENABLED=0, so cross-compilation needs no C toolchain.
BINARY  := grotto
PKG     := ./cmd/grotto
DIST    := dist
# -s -w strip the symbol table and DWARF info, keeping the binary well under the
# 25 MB target without affecting behavior.
LDFLAGS := -s -w

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
