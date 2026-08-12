# Five-minute trace redaction preview demo

This demo uses committed synthetic values only and performs no network calls.

```bash
tmp_dir="$(mktemp -d)"
cp tests/fixtures/redaction/synthetic-trace.json "$tmp_dir/trace.json"
before="$(shasum -a 256 "$tmp_dir/trace.json")"

go run ./cmd/grotto redact-preview --file "$tmp_dir/trace.json"
go run ./cmd/grotto redact-preview --file "$tmp_dir/trace.json" --json > "$tmp_dir/report.json"

after="$(shasum -a 256 "$tmp_dir/trace.json")"
test "$before" = "$after"
! rg -n 'test-token-never-real|operator@example\.test|Synthetic prompt only|/Users/example' "$tmp_dir/report.json"
```

The human preview should include retained, masked, hashed, truncated, and dropped rows. The JSON report should identify `grotto.redaction-preview.v1`, the exact policy SHA-256, rule provenance, and zero raw sentinel matches.

Run the edge policy and malformed/depth fixtures:

```bash
go run ./cmd/grotto redact-preview \
  --file tests/fixtures/redaction/edge-cases-trace.json \
  --policy tests/fixtures/redaction/conflict-policy.json --json
```

Finish with the local gates:

```bash
gofmt -w internal cmd
go test ./...
CGO_ENABLED=0 go build ./...
golangci-lint run ./...
```

`golangci-lint` is reported as unavailable rather than installed when it is not already present. Remove the temporary directory when finished.
