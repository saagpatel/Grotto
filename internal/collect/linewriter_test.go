package collect

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectLines returns an onLine sink and a pointer to the slice it fills, copying
// each line (lineWriter may reuse its buffer, so the test must not alias it).
func collectLines() (func([]byte), *[]string) {
	var got []string
	return func(line []byte) { got = append(got, string(line)) }, &got
}

func TestLineWriter_SplitsOnNewline(t *testing.T) {
	onLine, got := collectLines()
	w := newLineWriter(onLine)

	n, err := w.Write([]byte("alpha\nbeta\ngamma\n"))
	require.NoError(t, err)
	assert.Equal(t, len("alpha\nbeta\ngamma\n"), n, "Write must report all bytes consumed")
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, *got)
}

func TestLineWriter_ReassemblesAcrossWrites(t *testing.T) {
	// A single logical line split across arbitrary Write chunk boundaries (as
	// os/exec's copy loop would deliver a large event) must emit exactly once.
	onLine, got := collectLines()
	w := newLineWriter(onLine)

	for _, chunk := range []string{"he", "ll", "o-wor", "ld\nnext-"} {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"hello-world"}, *got, "fragments before the newline join into one line")

	_, err := w.Write([]byte("line\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"hello-world", "next-line"}, *got)
}

func TestLineWriter_FlushEmitsTrailingPartialLine(t *testing.T) {
	// A stream not terminated by a newline (panic/timeout truncation) still yields
	// its final line via flush.
	onLine, got := collectLines()
	w := newLineWriter(onLine)

	_, err := w.Write([]byte("done\ntrailing-no-newline"))
	require.NoError(t, err)
	assert.Equal(t, []string{"done"}, *got, "no trailing line until flush")

	w.flush()
	assert.Equal(t, []string{"done", "trailing-no-newline"}, *got)
}

func TestLineWriter_FlushNoTrailingIsNoop(t *testing.T) {
	onLine, got := collectLines()
	w := newLineWriter(onLine)
	_, err := w.Write([]byte("a\nb\n"))
	require.NoError(t, err)
	w.flush()
	assert.Equal(t, []string{"a", "b"}, *got, "flush with no partial line emits nothing extra")
}

func TestLineWriter_DropsOverLongLine(t *testing.T) {
	// An oversized line (a test dumping a giant blob in one output event) is dropped
	// to keep per-line memory bounded, but parsing resumes cleanly on the next line.
	onLine, got := collectLines()
	w := newLineWriter(onLine)

	huge := strings.Repeat("x", maxLineBytes+10)
	// Deliver the giant line in pieces with no newline, then a newline, then a real line.
	_, err := w.Write([]byte("keep-before\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte(huge[:maxLineBytes/2]))
	require.NoError(t, err)
	_, err = w.Write([]byte(huge[maxLineBytes/2:])) // crosses the cap -> drop mode
	require.NoError(t, err)
	_, err = w.Write([]byte("\nkeep-after\n")) // newline ends the dropped line
	require.NoError(t, err)

	assert.Equal(t, []string{"keep-before", "keep-after"}, *got,
		"the over-long line is dropped; lines on either side survive")
	assert.LessOrEqual(t, len(w.buf), maxLineBytes, "buffer never exceeds the cap")
}

func TestLineWriter_DroppedTrailingNotFlushed(t *testing.T) {
	// An over-long line with no terminating newline must not be emitted by flush.
	onLine, got := collectLines()
	w := newLineWriter(onLine)

	_, err := w.Write([]byte(strings.Repeat("y", maxLineBytes+1)))
	require.NoError(t, err)
	w.flush()
	assert.Empty(t, *got, "a dropped, unterminated line is discarded, not flushed")
}
