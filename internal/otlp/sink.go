package otlp

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/saagpatel/grotto/internal/model"
)

// DefaultBufferSize is the capacity of the sink's trace channel.
const DefaultBufferSize = 1024

// Sink decouples receiving from storage: receivers Submit traces onto a buffered
// channel that a single writer goroutine drains into the store. Serializing all
// writes through one goroutine (atop the store's single connection) keeps SQLite
// free of "database is locked" contention under concurrent exports.
type Sink struct {
	ch     chan model.Trace
	done   chan struct{}
	wg     sync.WaitGroup
	st     traceStore
	out    io.Writer
	errOut io.Writer
}

// traceStore is the slice of the store the sink needs (eases testing).
type traceStore interface {
	InsertTrace(ctx context.Context, t model.Trace) error
}

// NewSink starts the writer goroutine and returns a ready sink. Each stored
// trace is announced on out; storage errors are written to errOut (nil or
// io.Discard ignores either stream). Close must be called to drain and stop the
// writer.
func NewSink(st traceStore, bufSize int, out, errOut io.Writer) *Sink {
	if bufSize <= 0 {
		bufSize = DefaultBufferSize
	}
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	s := &Sink{
		ch:     make(chan model.Trace, bufSize),
		done:   make(chan struct{}),
		st:     st,
		out:    out,
		errOut: errOut,
	}
	s.wg.Add(1)
	go s.run()
	return s
}

// Submit queues traces for storage. After Close it drops further traces rather
// than blocking, so a shutting-down receiver never deadlocks.
func (s *Sink) Submit(traces []model.Trace) {
	for _, tr := range traces {
		select {
		case s.ch <- tr:
		case <-s.done:
			return
		}
	}
}

// Close stops accepting new traces, drains those already buffered, and waits for
// the writer to finish. The channel is never closed, so a Submit racing with
// Close takes the done branch instead of panicking on a closed channel.
//
// Callers must ensure no Submit is in flight when Close is called (e.g. by
// stopping the receivers first, as Serve does via GracefulStop). A Submit that
// genuinely races Close may deposit a trace after the final drain and lose it.
func (s *Sink) Close() {
	close(s.done)
	s.wg.Wait()
}

func (s *Sink) run() {
	defer s.wg.Done()
	for {
		select {
		case tr := <-s.ch:
			s.insert(tr)
		case <-s.done:
			s.drain()
			return
		}
	}
}

// drain stores any traces buffered at shutdown.
func (s *Sink) drain() {
	for {
		select {
		case tr := <-s.ch:
			s.insert(tr)
		default:
			return
		}
	}
}

func (s *Sink) insert(tr model.Trace) {
	// A fresh context: storage must complete even if an export's context is done.
	if err := s.st.InsertTrace(context.Background(), tr); err != nil {
		_, _ = fmt.Fprintf(s.errOut, "grotto: store trace %s: %v\n", tr.TraceID, err)
		return
	}
	// Announce receipt so an interactive `grotto serve` confirms each export
	// without the operator having to switch to `grotto list`. The full trace ID
	// is printed so it can be pasted straight into `grotto show`.
	_, _ = fmt.Fprintf(s.out, "grotto serve: received trace %s (%d spans, %s)\n",
		tr.TraceID, tr.SpanCount, tr.RunLabel)
}
