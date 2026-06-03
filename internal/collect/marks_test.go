package collect

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

func TestAssembleTrace_MarksBecomeChildSpans(t *testing.T) {
	marks := []Mark{
		{Name: "test", TSNs: 350}, // intentionally unsorted
		{Name: "setup", TSNs: 100},
		{Name: "compile", TSNs: 200},
	}
	tr := assembleTrace([]string{"/path/build.sh", "--release"}, marks, 50, 500, model.StatusOk)

	assert.Equal(t, 4, tr.SpanCount, "1 root + 3 marks")
	require.Len(t, tr.Spans, 4)
	assert.Equal(t, "mark", tr.Source)
	assert.Equal(t, "build.sh", tr.RootName)
	assert.Equal(t, "/path/build.sh --release", tr.RunLabel)

	root := tr.Spans[0]
	assert.Empty(t, root.ParentSpanID)
	assert.Equal(t, int64(50), root.StartedNs)
	assert.Equal(t, int64(500), root.EndedNs)

	// Children are sorted by timestamp; each ends at the next mark, the last at
	// the command's end time.
	setup, compile, test := tr.Spans[1], tr.Spans[2], tr.Spans[3]
	assert.Equal(t, "setup", setup.Name)
	assert.Equal(t, int64(100), setup.StartedNs)
	assert.Equal(t, int64(200), setup.EndedNs)
	assert.Equal(t, "compile", compile.Name)
	assert.Equal(t, int64(200), compile.StartedNs)
	assert.Equal(t, int64(350), compile.EndedNs)
	assert.Equal(t, "test", test.Name)
	assert.Equal(t, int64(350), test.StartedNs)
	assert.Equal(t, int64(500), test.EndedNs)

	for _, s := range tr.Spans[1:] {
		assert.Equal(t, root.SpanID, s.ParentSpanID, "every mark span parents to the root")
	}
}

func TestEmit_OverSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)

	c := &collector{}
	c.wg.Add(1)
	go c.serve(ln)

	t.Setenv(EnvSock, sock)
	t.Setenv(EnvSpool, "")
	require.NoError(t, Emit("compile"))
	require.NoError(t, Emit("test"))

	require.NoError(t, ln.Close())
	c.wg.Wait()

	marks := c.snapshot()
	require.Len(t, marks, 2)
	assert.ElementsMatch(t, []string{"compile", "test"},
		[]string{marks[0].Name, marks[1].Name})
}

func TestEmit_SpoolFallback(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool.jsonl")
	t.Setenv(EnvSock, filepath.Join(dir, "missing.sock")) // dial fails -> spool
	t.Setenv(EnvSpool, spool)

	require.NoError(t, Emit("a"))
	require.NoError(t, Emit("b"))

	marks, err := readSpool(spool)
	require.NoError(t, err)
	require.Len(t, marks, 2)
	assert.ElementsMatch(t, []string{"a", "b"},
		[]string{marks[0].Name, marks[1].Name})
}

func TestEmit_OutsideRun(t *testing.T) {
	t.Setenv(EnvSock, "")
	t.Setenv(EnvSpool, "")
	assert.Error(t, Emit("x"))
}

func TestEmit_ConcurrentOverSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)

	c := &collector{}
	c.wg.Add(1)
	go c.serve(ln)

	t.Setenv(EnvSock, sock)
	t.Setenv(EnvSpool, "")

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, Emit(fmt.Sprintf("mark-%d", i)))
		}(i)
	}
	wg.Wait()

	require.NoError(t, ln.Close())
	c.wg.Wait()

	assert.Len(t, c.snapshot(), n, "every concurrent mark is acknowledged and recorded")
}

func TestDedupMarks(t *testing.T) {
	in := []Mark{
		{Name: "a", TSNs: 1},
		{Name: "a", TSNs: 1}, // exact duplicate (same socket+spool mark)
		{Name: "a", TSNs: 2}, // same name, different time -> distinct
		{Name: "b", TSNs: 1},
	}
	out := dedupMarks(in)
	require.Len(t, out, 3)
	assert.Equal(t, []Mark{{Name: "a", TSNs: 1}, {Name: "a", TSNs: 2}, {Name: "b", TSNs: 1}}, out)
}

// TestRun_CollectsMarksOverSocket is the end-to-end plumbing test: it builds the
// grotto binary, runs a shell script that calls `grotto mark` five times, and
// asserts the run stored one root with five child spans.
func TestRun_CollectsMarksOverSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the grotto binary and spawns subprocesses")
	}
	ctx := context.Background()
	bin := buildGrotto(t)

	script := filepath.Join(t.TempDir(), "build.sh")
	body := "#!/bin/sh\n"
	for _, name := range []string{"setup", "compile", "test", "package", "report"} {
		body += `"$GROTTO_BIN" mark ` + name + "\nsleep 0.02\n"
	}
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("GROTTO_BIN", bin)

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grotto.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := Run(ctx, st, []string{script})
	require.NoError(t, err)

	tr, err := st.GetTrace(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 6, tr.SpanCount, "1 root + 5 marks")

	var roots, children int
	for _, s := range tr.Spans {
		if s.ParentSpanID == "" {
			roots++
		} else {
			children++
			assert.NotEmpty(t, s.Name)
		}
	}
	assert.Equal(t, 1, roots)
	assert.Equal(t, 5, children)
}

func buildGrotto(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "grotto")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/saagpatel/grotto/cmd/grotto")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build grotto: %v\n%s", err, out)
	}
	return bin
}
