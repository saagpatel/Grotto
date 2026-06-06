package otlp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/saagpatel/grotto/internal/model"
)

// memStore is a minimal traceStore for sink tests; failOn makes InsertTrace
// return an error for a given trace ID.
type memStore struct {
	failOn string
}

func (m *memStore) InsertTrace(_ context.Context, t model.Trace) error {
	if t.TraceID == m.failOn {
		return errors.New("boom")
	}
	return nil
}

func TestSink_AnnouncesReceiptOnSuccess(t *testing.T) {
	var out, errs bytes.Buffer
	sink := NewSink(&memStore{}, 0, &out, &errs)
	sink.Submit([]model.Trace{{TraceID: "abc123", SpanCount: 5, RunLabel: "checkout-api · POST /checkout"}})
	sink.Close()

	got := out.String()
	assert.Contains(t, got, "received trace abc123", "the full trace id is announced for copy-paste into `grotto show`")
	assert.Contains(t, got, "5 spans")
	assert.Contains(t, got, "checkout-api · POST /checkout")
	assert.Empty(t, errs.String(), "a successful store writes nothing to errOut")
}

func TestSink_StoreErrorGoesToErrOutNotOut(t *testing.T) {
	var out, errs bytes.Buffer
	sink := NewSink(&memStore{failOn: "bad"}, 0, &out, &errs)
	sink.Submit([]model.Trace{{TraceID: "bad", SpanCount: 1}})
	sink.Close()

	assert.Contains(t, errs.String(), "bad", "the failing trace id is reported on errOut")
	assert.NotContains(t, out.String(), "received trace", "a failed store is not announced as received")
}

func TestSink_NilWritersAreSafe(t *testing.T) {
	sink := NewSink(&memStore{}, 0, nil, nil)
	sink.Submit([]model.Trace{{TraceID: "x", SpanCount: 1}})
	sink.Close() // must not panic on nil out/errOut
}

func TestSink_OneLinePerTrace(t *testing.T) {
	var out bytes.Buffer
	sink := NewSink(&memStore{}, 0, &out, io.Discard)
	sink.Submit([]model.Trace{
		{TraceID: "t1", SpanCount: 2, RunLabel: "a"},
		{TraceID: "t2", SpanCount: 3, RunLabel: "b"},
	})
	sink.Close()

	lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1
	assert.Equal(t, 2, lines, "exactly one receipt line per stored trace")
}
