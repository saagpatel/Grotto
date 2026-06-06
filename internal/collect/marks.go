// Package collect implements the demarcated-marks capture path: `grotto run`
// executes a child command while `grotto mark <name>` calls (emitted from inside
// that command) stream span boundaries back over a Unix domain socket, with a
// JSONL spool file as a fallback. The collected marks are assembled into one
// OpenTelemetry trace rooted at the command.
//
// A mark is a demarcation point: each mark opens a child span that runs until
// the next mark, or until the command exits for the final mark. N marks therefore
// produce N child spans under a single root span (N+1 spans total).
//
// A mark may carry --child, nesting it one level under the most recent non-child
// mark (its "section") rather than parenting to the root. A child span runs until
// the very next mark — a sibling child or the next section — exactly subdividing
// its parent section's interval. Nesting is one level deep by design.
package collect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

// Environment variables the run injects for child `grotto mark` calls.
const (
	EnvSock  = "GROTTO_SOCK"  // Unix domain socket the run listens on
	EnvSpool = "GROTTO_SPOOL" // JSONL fallback path when the socket is unreachable
)

// connTimeout bounds how long the collector spends reading a single mark and
// writing its acknowledgement, so a stalled connection cannot hang a handler.
const connTimeout = 2 * time.Second

// Mark is one demarcation point emitted by `grotto mark <name>`. Child marks
// (`grotto mark <name> --child`) nest one level under the most recent non-child
// mark instead of parenting to the root. Mark stays comparable so it can key the
// dedup set directly.
type Mark struct {
	Name  string `json:"name"`
	TSNs  int64  `json:"ts_ns"`
	Child bool   `json:"child,omitempty"`
}

// Run executes argv[0] with argv[1:], collecting marks over a Unix domain socket
// (JSONL spool fallback), assembles them into a single trace rooted at the
// command, persists it, and returns the trace ID. A non-zero child exit is
// recorded as an error status on the root span rather than failing the capture.
//
// ad is an optional build-tool adapter. When non-nil, Run injects any flags
// the adapter needs (via PrepareArgv), tees stderr so the adapter can scan it
// for the timing report path, then calls ParseSpans after the command exits and
// grafts the returned per-unit spans onto the trace. Passing nil preserves the
// existing behavior exactly — no flags injected, no stderr captured.
func Run(ctx context.Context, st *store.Store, argv []string, ad adapter.Adapter) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("no command given")
	}

	// Let the adapter prepend/append any flags it needs before we build the Cmd.
	if ad != nil {
		argv = ad.PrepareArgv(argv)
	}

	dir, err := os.MkdirTemp("", "grotto-run-")
	if err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	sockPath := filepath.Join(dir, "sock")
	spoolPath := filepath.Join(dir, "spool.jsonl")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", fmt.Errorf("listen on %q: %w", sockPath, err)
	}

	c := &collector{}
	c.wg.Add(1)
	go c.serve(ln) // owner: this function; exits when ln is closed below

	startNs := time.Now().UnixNano()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(), EnvSock+"="+sockPath, EnvSpool+"="+spoolPath)

	// When an adapter is active we tee stderr into a buffer so the adapter can
	// scan it (cargo reads the timing-report path here). The user still sees the
	// live output because MultiWriter writes to os.Stderr first. When no adapter
	// is active, wire directly so behavior is unchanged.
	var stderrBuf, stdoutBuf bytes.Buffer
	switch {
	case ad == nil:
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	case ad.CapturesStdout():
		// The adapter consumes stdout as a machine-readable stream (go test
		// -json), so capture it silently rather than scrolling raw JSON past
		// the user; stderr (build/vet errors) still passes through.
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	default:
		cmd.Stdout = os.Stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	}

	runErr := cmd.Run()
	endNs := time.Now().UnixNano()

	// Stop accepting and wait for in-flight marks to finish being read.
	_ = ln.Close()
	c.wg.Wait()

	marks := c.snapshot()
	spooled, err := readSpool(spoolPath)
	if err != nil {
		return "", fmt.Errorf("read spool: %w", err)
	}
	// A mark may reach both the socket and the spool (e.g. a slow ack); each is
	// identified by its (name, timestamp, child), so dedup collapses it to one span.
	marks = dedupMarks(append(marks, spooled...))

	status := model.StatusOk
	if runErr != nil {
		status = model.StatusError
	}
	tr := assembleTrace(argv, marks, startNs, endNs, status)

	// When an adapter is active, parse per-unit spans from the timing report and
	// graft them onto the trace. The root span is always tr.Spans[0].
	if ad != nil {
		bc := adapter.BuildContext{
			RootID:    tr.Spans[0].SpanID,
			TraceID:   tr.TraceID,
			StartNs:   startNs,
			EndNs:     endNs,
			Stderr:    stderrBuf.Bytes(),
			Stdout:    stdoutBuf.Bytes(),
			NewSpanID: newSpanID,
		}
		adSpans, parseErr := ad.ParseSpans(context.WithoutCancel(ctx), bc)
		if parseErr != nil {
			// The per-unit spans are an enrichment, not the capture itself: the
			// command already ran and its root/marks trace is assembled. A failure
			// to parse the timing report must not discard that trace, so warn and
			// fall through to store the base trace rather than returning an error.
			fmt.Fprintf(os.Stderr, "grotto: adapter %q: %v (storing trace without per-unit spans)\n", ad.Name(), parseErr)
		} else if len(adSpans) > 0 {
			tr.Spans = append(tr.Spans, adSpans...)
			tr.SpanCount = len(tr.Spans)
			tr.Source = ad.Name()
		}
	}

	// Persist with a non-cancelable context so a trace is still saved even when
	// the run was interrupted (which would have canceled ctx).
	if err := st.InsertTrace(context.WithoutCancel(ctx), tr); err != nil {
		return "", fmt.Errorf("store trace: %w", err)
	}
	return tr.TraceID, nil
}

// collector accumulates marks received on the run socket.
type collector struct {
	wg    sync.WaitGroup
	mu    sync.Mutex
	marks []Mark
}

// serve accepts connections until the listener is closed, handling each in its
// own goroutine so concurrent marks (e.g. from a parallel build) never block one
// another behind a single read. All handlers are drained before serve returns,
// so a caller that closes the listener and waits on the collector's WaitGroup
// sees every recorded mark.
func (c *collector) serve(ln net.Listener) {
	defer c.wg.Done()

	var handlers sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			break // listener closed
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			c.handle(conn)
		}()
	}
	handlers.Wait()
}

// handle reads one mark from conn, records it, then writes a one-byte
// acknowledgement so the emitter knows the mark is durably held before its
// `grotto mark` process exits.
func (c *collector) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(connTimeout))

	var m Mark
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&m); err != nil {
		return // ignore malformed or timed-out marks (no ack -> emitter spools)
	}
	c.mu.Lock()
	c.marks = append(c.marks, m)
	c.mu.Unlock()

	_, _ = conn.Write([]byte{1}) // ack; best-effort, mark is already recorded
}

func (c *collector) snapshot() []Mark {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Mark, len(c.marks))
	copy(out, c.marks)
	return out
}

// dedupMarks removes marks that are identical in both name and timestamp,
// preserving first-seen order. Mark is comparable, so it keys the set directly.
func dedupMarks(marks []Mark) []Mark {
	seen := make(map[Mark]struct{}, len(marks))
	out := marks[:0]
	for _, m := range marks {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// readSpool reads marks from the JSONL spool file, tolerating a missing file
// (the common case, when every mark reached the socket) and skipping any
// malformed lines.
func readSpool(path string) ([]Mark, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open spool %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var marks []Mark
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m Mark
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		marks = append(marks, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan spool %q: %w", path, err)
	}
	return marks, nil
}

// assembleTrace turns a set of marks into a trace: one root span covering the
// whole command, and one child span per mark spanning from that mark to the next
// (or to the command's end for the final mark).
func assembleTrace(argv []string, marks []Mark, startNs, endNs int64, status model.StatusCode) model.Trace {
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].TSNs < marks[j].TSNs })

	traceID := newTraceID()
	rootID := newSpanID()
	rootName := filepath.Base(argv[0])

	// newSpan builds an internal-kind span, deriving duration from the bounds.
	newSpan := func(id, parentID, name string, st model.StatusCode, startNs, endNs int64) model.Span {
		return model.Span{
			SpanID:       id,
			TraceID:      traceID,
			ParentSpanID: parentID,
			Name:         name,
			Kind:         model.KindInternal,
			Status:       st,
			StartedNs:    startNs,
			EndedNs:      endNs,
			DurationNs:   endNs - startNs,
		}
	}

	spans := make([]model.Span, 0, len(marks)+1)
	spans = append(spans, newSpan(rootID, "", rootName, status, startNs, endNs))

	// sectionID is the parent for the next --child mark: the most recent non-child
	// mark's span, or the root when no section is open yet (so a leading --child
	// degrades gracefully to a root child rather than dangling).
	sectionID := rootID
	for i, m := range marks {
		spanStart := m.TSNs
		if spanStart < startNs {
			spanStart = startNs
		}

		var parentID string
		var spanEnd int64
		if m.Child {
			// A child ends at the very next mark — a sibling child or the next
			// section — so its interval subdivides the current section.
			parentID = sectionID
			spanEnd = endNs
			if i+1 < len(marks) {
				spanEnd = marks[i+1].TSNs
			}
		} else {
			// A section parents to the root and runs until the next non-child
			// mark (the next section), or the command's end.
			parentID = rootID
			spanEnd = endNs
			for j := i + 1; j < len(marks); j++ {
				if !marks[j].Child {
					spanEnd = marks[j].TSNs
					break
				}
			}
		}
		if spanEnd < spanStart {
			spanEnd = spanStart
		}

		id := newSpanID()
		spans = append(spans, newSpan(id, parentID, m.Name, model.StatusOk, spanStart, spanEnd))
		if !m.Child {
			sectionID = id // subsequent --child marks nest under this section
		}
	}

	return model.Trace{
		TraceID:    traceID,
		RunLabel:   strings.Join(argv, " "),
		Source:     "mark",
		RootName:   rootName,
		StartedNs:  startNs,
		EndedNs:    endNs,
		DurationNs: endNs - startNs,
		SpanCount:  len(spans),
		Spans:      spans,
	}
}

// newTraceID returns a 16-byte (32 hex char) OpenTelemetry trace ID.
func newTraceID() string { return randHex(16) }

// newSpanID returns an 8-byte (16 hex char) OpenTelemetry span ID.
func newSpanID() string { return randHex(8) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms; if it ever does,
		// fall back to a time-derived value so a trace is still distinguishable
		// rather than panicking in library code.
		ts := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ts >> uint(8*(i%8)))
		}
	}
	return hex.EncodeToString(b)
}
