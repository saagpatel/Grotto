package tui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

// keyMsg builds a KeyMsg whose String() matches the model's switch cases:
// special keys map to their KeyType, everything else is a rune key.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// fourSpanTrace is a small nested trace: root → {a → a1, b}. Pre-order by start
// time is [root, a, a1, b].
func fourSpanTrace() model.Trace {
	spans := []model.Span{
		{SpanID: "root", Name: "root", StartedNs: 0, EndedNs: 100, DurationNs: 100, Kind: model.KindInternal, Status: model.StatusOk},
		{SpanID: "a", ParentSpanID: "root", Name: "a", StartedNs: 10, EndedNs: 50, DurationNs: 40, Kind: model.KindClient, Status: model.StatusOk},
		{SpanID: "a1", ParentSpanID: "a", Name: "a1", StartedNs: 20, EndedNs: 40, DurationNs: 20, Kind: model.KindInternal, Status: model.StatusError},
		{SpanID: "b", ParentSpanID: "root", Name: "b", StartedNs: 60, EndedNs: 90, DurationNs: 30, Kind: model.KindInternal, Status: model.StatusOk},
	}
	return model.Trace{
		TraceID: "t1", RunLabel: "build", Source: "mark", RootName: "root",
		StartedNs: 0, EndedNs: 100, DurationNs: 100, SpanCount: 4, Spans: spans,
	}
}

// asModel unwraps the tea.Model returned by Update back to the concrete root
// model (two-value assertion keeps errcheck's check-type-assertions happy).
func asModel(t *testing.T, m tea.Model) Model {
	t.Helper()
	rm, ok := m.(Model)
	require.True(t, ok, "Update must return the root Model")
	return rm
}

type fakeSource struct {
	summaries []store.TraceSummary
	trace     model.Trace
	err       error
}

func (f *fakeSource) RecentTraces(_ context.Context, _ int) ([]store.TraceSummary, error) {
	return f.summaries, f.err
}

func (f *fakeSource) GetTrace(_ context.Context, _ string) (model.Trace, error) {
	return f.trace, f.err
}

func TestRunList_Navigation(t *testing.T) {
	m := newRunListModel().setRows([]store.TraceSummary{{TraceID: "a"}, {TraceID: "b"}, {TraceID: "c"}})

	m, _ = m.Update(keyMsg("down"))
	m, _ = m.Update(keyMsg("down"))
	assert.Equal(t, 2, m.cursor)

	m, _ = m.Update(keyMsg("down")) // clamp at end
	assert.Equal(t, 2, m.cursor)

	m, _ = m.Update(keyMsg("up"))
	m, _ = m.Update(keyMsg("up"))
	m, _ = m.Update(keyMsg("up")) // clamp at start
	assert.Equal(t, 0, m.cursor)
}

func TestRunList_EnterSelectsCursorTrace(t *testing.T) {
	m := newRunListModel().setRows([]store.TraceSummary{{TraceID: "x"}, {TraceID: "y"}})
	m, _ = m.Update(keyMsg("down"))

	_, cmd := m.Update(keyMsg("enter"))
	require.NotNil(t, cmd)
	sel, ok := cmd().(selectTraceMsg)
	require.True(t, ok, "enter must emit selectTraceMsg")
	assert.Equal(t, "y", sel.traceID)
}

func TestRunList_EnterEmptyIsNoop(t *testing.T) {
	_, cmd := newRunListModel().Update(keyMsg("enter"))
	assert.Nil(t, cmd, "enter on an empty list must not emit a command")
}

func TestWaterfall_CollapseRootHidesAll(t *testing.T) {
	m := newWaterfallModel(fourSpanTrace(), 100, 30)
	require.Len(t, m.visible, 4)

	m, _ = m.Update(keyMsg(" ")) // cursor on root → collapse
	assert.Len(t, m.visible, 1, "collapsing the root hides every descendant")

	m, _ = m.Update(keyMsg(" ")) // expand
	assert.Len(t, m.visible, 4)
}

func TestWaterfall_CollapseSubtree(t *testing.T) {
	m := newWaterfallModel(fourSpanTrace(), 100, 30)

	m, _ = m.Update(keyMsg("down")) // cursor → "a"
	require.Equal(t, 1, m.cursor)

	m, _ = m.Update(keyMsg(" ")) // collapse "a" hides "a1"
	assert.Len(t, m.visible, 3)
}

func TestWaterfall_EnterOpensInspectorForCursorSpan(t *testing.T) {
	m := newWaterfallModel(fourSpanTrace(), 100, 30)
	m, _ = m.Update(keyMsg("down")) // cursor → "a"

	_, cmd := m.Update(keyMsg("enter"))
	require.NotNil(t, cmd)
	open, ok := cmd().(openInspectorMsg)
	require.True(t, ok, "enter must emit openInspectorMsg")
	assert.Equal(t, "a", open.span.SpanID)
}

func TestInspector_RendersIdentityLabelsAndAttributes(t *testing.T) {
	sp := model.Span{
		SpanID: "a", Name: "compile", Kind: model.KindClient, Status: model.StatusError,
		DurationNs: 190 * int64(time.Millisecond),
		Attributes: []model.Attribute{{Key: "jobs", ValueType: "int", Value: "8"}},
	}
	v := newInspectorModel(sp, 80, 24).View()

	for _, want := range []string{"compile", "client", "error", "jobs", "8", "190ms"} {
		assert.Contains(t, v, want)
	}
}

func TestRoot_RunListToWaterfallTransition(t *testing.T) {
	src := &fakeSource{
		summaries: []store.TraceSummary{{TraceID: "t1", RunLabel: "build", SpanCount: 4}},
		trace:     fourSpanTrace(),
	}
	m := New(context.Background(), src)

	// Size the terminal, then deliver the loaded trace list.
	rm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = asModel(t, rm)
	rm, _ = m.Update(tracesLoadedMsg{summaries: src.summaries})
	m = asModel(t, rm)
	require.Equal(t, screenRunList, m.screen)

	// Enter → selectTraceMsg → loadTrace cmd → traceLoadedMsg → waterfall.
	rm, cmd := m.Update(keyMsg("enter"))
	m = asModel(t, rm)
	require.NotNil(t, cmd)
	sel, ok := cmd().(selectTraceMsg)
	require.True(t, ok)
	assert.Equal(t, "t1", sel.traceID)

	rm, cmd = m.Update(sel)
	m = asModel(t, rm)
	require.NotNil(t, cmd)
	loaded, ok := cmd().(traceLoadedMsg)
	require.True(t, ok)
	require.NoError(t, loaded.err)

	rm, _ = m.Update(loaded)
	m = asModel(t, rm)
	assert.Equal(t, screenWaterfall, m.screen)
	assert.Len(t, m.waterfall.visible, 4)
}

func TestRoot_BackPopsScreens(t *testing.T) {
	m := New(context.Background(), &fakeSource{trace: fourSpanTrace()})
	m.waterfall = newWaterfallModel(fourSpanTrace(), 100, 30)
	m.inspector = newInspectorModel(fourSpanTrace().Spans[1], 100, 30)
	m.screen = screenInspector

	rm, _ := m.Update(keyMsg("esc"))
	m = asModel(t, rm)
	assert.Equal(t, screenWaterfall, m.screen, "esc from inspector returns to waterfall")

	rm, _ = m.Update(keyMsg("esc"))
	m = asModel(t, rm)
	assert.Equal(t, screenRunList, m.screen, "esc from waterfall returns to run list")
}

func TestRoot_QuitKey(t *testing.T) {
	m := New(context.Background(), &fakeSource{})
	_, cmd := m.Update(keyMsg("q"))
	require.NotNil(t, cmd)
	assert.Equal(t, tea.Quit(), cmd(), "q must issue the quit command")
}

func TestHumanAge_Buckets(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ago := func(sec int64) int64 { return now.Add(-time.Duration(sec) * time.Second).UnixNano() }

	assert.Equal(t, "30s ago", humanAge(ago(30), now))
	assert.Equal(t, "5m ago", humanAge(ago(300), now))
	assert.Equal(t, "2h ago", humanAge(ago(7200), now))
	assert.Equal(t, "3d ago", humanAge(ago(259200), now))
	assert.Equal(t, "0s ago", humanAge(now.Add(time.Hour).UnixNano(), now), "future timestamps clamp to 0")
}

// gen200SpanTrace builds a 200-span trace (one root + 199 children) for the
// navigation performance acceptance check.
func gen200SpanTrace() model.Trace {
	const n = 200
	spans := make([]model.Span, 0, n)
	spans = append(spans, model.Span{
		SpanID: "root", Name: "root", StartedNs: 0, EndedNs: 200_000, DurationNs: 200_000,
		Kind: model.KindInternal, Status: model.StatusOk,
	})
	for i := 1; i < n; i++ {
		st := int64(i) * 100
		spans = append(spans, model.Span{
			SpanID: fmt.Sprintf("s%d", i), ParentSpanID: "root", Name: fmt.Sprintf("span-%d", i),
			StartedNs: st, EndedNs: st + 50, DurationNs: 50,
			Kind: model.KindInternal, Status: model.StatusOk,
		})
	}
	return model.Trace{
		TraceID: "big", RunLabel: "big run", Source: "otlp", RootName: "root",
		StartedNs: 0, EndedNs: 200_000, DurationNs: 200_000, SpanCount: n, Spans: spans,
	}
}

func TestWaterfall_NavigationPerf200Spans(t *testing.T) {
	m := newWaterfallModel(gen200SpanTrace(), 120, 40)
	require.Len(t, m.visible, 200)

	const presses = 199
	start := time.Now()
	for i := 0; i < presses; i++ {
		m, _ = m.Update(keyMsg("down"))
	}
	perKey := time.Since(start) / presses

	require.Equal(t, 199, m.cursor, "cursor must reach the last span")
	assert.Lessf(t, perKey, 50*time.Millisecond,
		"navigation must stay under 50ms/keystroke; got %v/key", perKey)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5), "short strings pass through")
	assert.Equal(t, "ab…", truncate("abcdef", 3), "long strings get an ellipsis")
	assert.Equal(t, "café", truncate("café", 4), "rune-aware: no mid-character split")
}
