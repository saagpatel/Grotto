// Package tui is Grotto's interactive trace browser: a Bubble Tea application
// with three screens — a run list, a collapsible proportional waterfall, and a
// span inspector. It reads from the same store and reuses render.Layout, so the
// interactive and static (`grotto show`) views share one layout engine.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

// screen identifies which view is active.
type screen int

const (
	screenRunList screen = iota
	screenWaterfall
	screenInspector
)

// recentLimit is how many traces the run list loads (the roadmap's "last 50").
const recentLimit = 50

// traceSource is the slice of the store the TUI depends on. Narrowing to an
// interface keeps the model unit-testable with a fake and documents the exact
// surface the UI touches. *store.Store satisfies it.
type traceSource interface {
	RecentTraces(ctx context.Context, limit int) ([]store.TraceSummary, error)
	GetTrace(ctx context.Context, traceID string) (model.Trace, error)
}

// Messages threaded between the store, the root, and the screens. Store reads
// run as tea.Cmds so I/O never blocks Update; their results arrive as the
// *LoadedMsg variants. selectTraceMsg / openInspectorMsg are emitted by a screen
// to request a transition the root performs.
type (
	tracesLoadedMsg struct {
		summaries []store.TraceSummary
		err       error
	}
	traceLoadedMsg struct {
		trace model.Trace
		err   error
	}
	selectTraceMsg   struct{ traceID string }
	openInspectorMsg struct{ span model.Span }
)

// Model is the root Bubble Tea model: it owns the active screen, the three
// sub-models, shared terminal dimensions, and routes messages between them.
type Model struct {
	ctx    context.Context
	store  traceSource
	screen screen
	width  int
	height int

	runList   runListModel
	waterfall waterfallModel
	inspector inspectorModel

	loadErr error
}

// New builds the root model rooted on the run list. Init kicks off the first
// trace-list load.
func New(ctx context.Context, src traceSource) Model {
	return Model{
		ctx:     ctx,
		store:   src,
		screen:  screenRunList,
		runList: newRunListModel(),
	}
}

// Init loads the recent trace list.
func (m Model) Init() tea.Cmd {
	return m.loadRecent()
}

// loadRecent returns a command that loads the recent trace summaries.
func (m Model) loadRecent() tea.Cmd {
	return func() tea.Msg {
		summaries, err := m.store.RecentTraces(m.ctx, recentLimit)
		return tracesLoadedMsg{summaries: summaries, err: err}
	}
}

// loadTrace returns a command that loads one full trace by ID.
func (m Model) loadTrace(id string) tea.Cmd {
	return func() tea.Msg {
		tr, err := m.store.GetTrace(m.ctx, id)
		return traceLoadedMsg{trace: tr, err: err}
	}
}

// Update routes messages: window size and load results are handled centrally;
// global keys (quit, back) are intercepted; everything else is delegated to the
// active screen, whose returned commands may request a screen transition.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.runList = m.runList.setSize(msg.Width, msg.Height)
		m.waterfall = m.waterfall.setSize(msg.Width, msg.Height)
		m.inspector = m.inspector.setSize(msg.Width, msg.Height)
		return m, nil

	case tracesLoadedMsg:
		if msg.err != nil {
			m.loadErr = msg.err
			return m, nil
		}
		m.loadErr = nil
		m.runList = m.runList.setRows(msg.summaries)
		return m, nil

	case traceLoadedMsg:
		if msg.err != nil {
			m.loadErr = msg.err
			return m, nil
		}
		m.loadErr = nil
		m.waterfall = newWaterfallModel(msg.trace, m.width, m.height)
		m.screen = screenWaterfall
		return m, nil

	case selectTraceMsg:
		return m, m.loadTrace(msg.traceID)

	case openInspectorMsg:
		m.inspector = newInspectorModel(msg.span, m.width, m.height)
		m.screen = screenInspector
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc", "backspace":
			return m.goBack(), nil
		}
		return m.updateActive(msg)
	}

	// Non-key messages (e.g. viewport internals) go to the active screen too.
	return m.updateActive(msg)
}

// goBack pops one screen toward the run list. On the run list it is a no-op
// (quitting is handled by q/ctrl+c).
func (m Model) goBack() Model {
	switch m.screen {
	case screenInspector:
		m.screen = screenWaterfall
	case screenWaterfall:
		m.screen = screenRunList
	}
	return m
}

// updateActive forwards a message to the active screen, stores the updated
// sub-model back, and returns any command it produced.
func (m Model) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.screen {
	case screenRunList:
		m.runList, cmd = m.runList.Update(msg)
	case screenWaterfall:
		m.waterfall, cmd = m.waterfall.Update(msg)
	case screenInspector:
		m.inspector, cmd = m.inspector.Update(msg)
	}
	return m, cmd
}

// View renders the active screen, or a load error if one occurred.
func (m Model) View() string {
	if m.loadErr != nil {
		return fmt.Sprintf("\n  %s\n\n  %s\n",
			errStyle.Render("error: "+m.loadErr.Error()),
			helpStyle.Render("q quit"))
	}
	switch m.screen {
	case screenWaterfall:
		return m.waterfall.View()
	case screenInspector:
		return m.inspector.View()
	default:
		return m.runList.View()
	}
}

// Run launches the interactive browser against a store, using the alternate
// screen buffer so the terminal is restored on exit.
func Run(ctx context.Context, src traceSource) error {
	if _, err := tea.NewProgram(New(ctx, src), tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
