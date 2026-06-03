package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/saagpatel/grotto/internal/render"
	"github.com/saagpatel/grotto/internal/store"
)

// Shared styles for every screen. Colors are 256-color codes so they degrade
// gracefully on limited terminals.
var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// runListModel is Screen 1: a navigable list of recent traces. Selecting one
// emits a selectTraceMsg the root turns into a trace load.
type runListModel struct {
	rows   []store.TraceSummary
	cursor int
	width  int
	height int
	now    func() time.Time // injectable so age rendering is testable
}

func newRunListModel() runListModel {
	return runListModel{now: time.Now}
}

// setRows replaces the listed traces, clamping the cursor into range.
func (m runListModel) setRows(rows []store.TraceSummary) runListModel {
	m.rows = rows
	if m.cursor >= len(rows) {
		m.cursor = max(0, len(rows)-1)
	}
	return m
}

func (m runListModel) setSize(w, h int) runListModel {
	m.width, m.height = w, h
	return m
}

// Update handles list navigation and selection. Quit/back are handled by the
// root, so they are not consumed here.
func (m runListModel) Update(msg tea.Msg) (runListModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(0, len(m.rows)-1)
	case "enter":
		if len(m.rows) == 0 {
			return m, nil
		}
		id := m.rows[m.cursor].TraceID
		return m, func() tea.Msg { return selectTraceMsg{traceID: id} }
	}
	return m, nil
}

func (m runListModel) View() string {
	lines := []string{titleStyle.Render("grotto — traces"), ""}

	if len(m.rows) == 0 {
		lines = append(lines,
			dimStyle.Render("  no traces yet — capture one with `grotto run -- <cmd>` or point an exporter at `grotto serve`"),
			"",
			helpStyle.Render("  q quit"))
		return strings.Join(lines, "\n")
	}

	lines = append(lines, headerStyle.Render(
		fmt.Sprintf("  %-28s %6s  %10s  %-5s  %s", "LABEL", "SPANS", "DURATION", "SRC", "AGE")))

	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	for i, r := range m.rows {
		line := fmt.Sprintf("  %-28s %6d  %10s  %-5s  %s",
			truncate(r.RunLabel, 28), r.SpanCount,
			render.FormatDuration(r.DurationNs), truncate(r.Source, 5),
			render.HumanAge(r.CreatedAt, now))
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}

	lines = append(lines, "", helpStyle.Render("  ↑/↓ move · enter open · q quit"))
	return strings.Join(lines, "\n")
}

// truncate shortens s to at most n display cells, appending an ellipsis when it
// trims. It is rune-aware so multibyte names are not split mid-character.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
