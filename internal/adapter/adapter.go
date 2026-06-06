// Package adapter turns build-tool-native timing output into Grotto spans, so a
// build phase that owns its own compile loop (and is therefore opaque to `grotto
// mark`) becomes a per-unit waterfall. Each adapter knows how to inject the flags
// its tool needs and how to parse that tool's timing report into OTel child spans.
//
// The registry (adapters map) is the pluggability mechanism — adding a second
// adapter (go test -json, pytest) is a one-file addition; no plugin framework or
// interface discovery is needed for two consumers.
package adapter

import (
	"context"
	"sort"

	"github.com/saagpatel/grotto/internal/model"
)

// BuildContext carries everything an adapter's ParseSpans needs: the root span
// identity to parent child spans under, the time window of the full command run,
// the captured stderr output (searched for the report path), and a NewSpanID
// factory injected by the caller so the adapter never duplicates ID generation
// with the collect layer.
type BuildContext struct {
	// RootID is the SpanID of the root span (tr.Spans[0].SpanID) that adapter
	// spans are parented under.
	RootID string
	// TraceID is the trace ID that all adapter spans must carry.
	TraceID string
	// StartNs is the absolute Unix timestamp (nanoseconds) of the command's
	// start — used to anchor the adapter's build-relative times.
	StartNs int64
	// EndNs is the absolute Unix timestamp (nanoseconds) of the command's end.
	EndNs int64
	// Stderr is the captured standard error of the command, searched for the
	// timing report announcement line (used by the cargo adapter).
	Stderr []byte
	// Stdout is the captured standard output of the command, populated only when
	// the adapter's CapturesStdout reports true (used by the go-test adapter,
	// whose -json event stream is written to stdout).
	Stdout []byte
	// NewSpanID is a factory func for fresh span IDs, injected by the caller
	// (collect.newSpanID) so that ID generation stays centralized and tests can
	// supply a deterministic counter.
	NewSpanID func() string
}

// Adapter is the interface each build-tool adapter must satisfy. An adapter
// knows how to augment a command's argument list with the flags its tool needs
// to emit a timing report, and how to parse that report into Grotto spans after
// the command has exited.
type Adapter interface {
	// Name returns the adapter's registry key, used as Trace.Source when the
	// adapter is active (e.g. "cargo").
	Name() string

	// PrepareArgv injects any flags the tool needs (e.g. --timings for cargo)
	// into argv and returns the resulting slice. The implementation must be
	// idempotent — if the user already supplied the required flag, it must not
	// be added a second time.
	PrepareArgv(argv []string) []string

	// CapturesStdout reports whether the adapter consumes the child's stdout as
	// its data source. When true, Run captures stdout into BuildContext.Stdout
	// and suppresses its live passthrough (the stream is machine-readable, e.g.
	// `go test -json`). When false (cargo), stdout passes through to the user
	// untouched and BuildContext.Stdout is empty.
	CapturesStdout() bool

	// ParseSpans runs after the command exits. It reads the timing report
	// announced on bc.Stderr, parses it, and returns child spans parented under
	// bc.RootID, anchored to bc.StartNs. It MUST tolerate missing artifacts —
	// a failed build that emitted no report — by returning (nil, nil), so the
	// run degrades gracefully to just the root span rather than failing.
	ParseSpans(ctx context.Context, bc BuildContext) ([]model.Span, error)
}

// adapters is the global registry: flag value → Adapter. Adding an adapter is a
// one-line addition here plus a new file; no interface registration machinery needed.
var adapters = map[string]Adapter{
	"cargo":   cargoAdapter{},
	"go-test": goTestAdapter{},
}

// Names returns the registered adapter names in sorted order, for help text and
// error messages.
func Names() []string {
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup returns the registered adapter for name. ok is false both for unknown
// names (caller turns that into a user-facing error) and for the empty string
// (meaning "no adapter requested").
func Lookup(name string) (Adapter, bool) {
	if name == "" {
		return nil, false
	}
	a, ok := adapters[name]
	return a, ok
}
