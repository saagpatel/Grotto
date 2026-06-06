package collect

import "bytes"

// maxLineBytes caps how much a single newline-less line may accumulate before the
// lineWriter drops the remainder of that line. `go test -json`'s structural events
// (start/run/pass/fail/skip) are well under this; only an "output" event echoing a
// huge blob can exceed it, and the go-test adapter discards output events anyway.
// The cap keeps per-line memory bounded so the streaming guarantee is total — not
// "bounded except for one pathological line".
const maxLineBytes = 1 << 20 // 1 MiB

// lineWriter is an io.Writer that invokes onLine for each '\n'-terminated line and
// holds a trailing partial line until flush. Its retained buffer never grows past
// maxLineBytes (a newline-less run beyond the cap is dropped, not accumulated), so
// feeding it a multi-hundred-megabyte stream costs O(1) memory regardless of total
// size. Run wires it as cmd.Stdout for a streaming adapter; os/exec's internal copy
// goroutine drives Write, and cmd.Wait joins it, so every onLine call completes
// before Run materializes the trace — no goroutine the collector owns.
type lineWriter struct {
	onLine   func(line []byte)
	buf      []byte
	dropping bool // mid-drop of an over-long line: skip bytes until the next '\n'
}

func newLineWriter(onLine func(line []byte)) *lineWriter {
	return &lineWriter{onLine: onLine}
}

// Write splits p on newlines, emitting each complete line via onLine and buffering
// any remainder for the next call. It always reports len(p) consumed — an io.Writer
// feeding a process's stdout must never short-write — even when a pathological line
// is dropped.
func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.appendPartial(p)
			return n, nil
		}
		if w.dropping {
			// The newline ends the over-long line we were dropping; resume parsing.
			w.dropping = false
			w.buf = w.buf[:0]
		} else {
			w.emit(p[:i])
		}
		p = p[i+1:]
	}
}

// appendPartial buffers a newline-less chunk, entering drop mode if the line grows
// past the cap so retained memory stays bounded to maxLineBytes.
func (w *lineWriter) appendPartial(p []byte) {
	if w.dropping {
		return
	}
	if len(w.buf)+len(p) > maxLineBytes {
		w.dropping = true
		w.buf = w.buf[:0]
		return
	}
	w.buf = append(w.buf, p...)
}

// emit delivers one complete line: the buffered prefix (if any) plus seg. When the
// whole line arrived in a single Write, seg is handed through without a copy.
func (w *lineWriter) emit(seg []byte) {
	if len(w.buf) == 0 {
		w.onLine(seg)
		return
	}
	w.buf = append(w.buf, seg...)
	w.onLine(w.buf)
	w.buf = w.buf[:0]
}

// flush emits any trailing partial line — a stream that did not end in a newline,
// e.g. a panic-truncated run. Call once after the command exits. A line that was
// being dropped for exceeding the cap is discarded, not emitted.
func (w *lineWriter) flush() {
	if !w.dropping && len(w.buf) > 0 {
		w.onLine(w.buf)
	}
	w.buf = w.buf[:0]
	w.dropping = false
}
