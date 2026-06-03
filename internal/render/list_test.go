package render

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/store"
)

func TestWriteTraceList_RendersRowShape(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rows := []store.TraceSummary{
		{TraceID: "abc123", RunLabel: "make all", Source: "mark", DurationNs: 600 * ms, SpanCount: 6, CreatedAt: now.Add(-30 * time.Second).UnixNano()},
		{TraceID: "def456", RunLabel: "go test ./...", Source: "otlp", DurationNs: 1_500 * ms, SpanCount: 12, CreatedAt: now.Add(-2 * time.Hour).UnixNano()},
	}

	var buf bytes.Buffer
	require.NoError(t, WriteTraceList(&buf, rows, now))
	out := buf.String()

	for _, want := range []string{"TRACE", "SPANS", "abc123", "make all", "600ms", "30s ago", "def456", "otlp", "2h ago"} {
		assert.Contains(t, out, want)
	}
}

func TestWriteTraceList_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteTraceList(&buf, nil, time.Unix(0, 0)))
	assert.Contains(t, buf.String(), "no traces yet")
}
