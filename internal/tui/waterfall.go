package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/render"
)

// barRune is the filled timeline character (matches the static waterfall).
const barRune = "█"

// waterfallChrome is the number of terminal rows reserved for the title and help
// lines surrounding the scrollable viewport.
const waterfallChrome = 4

// Timeline width is derived from the terminal width minus the name column and
// margins, then clamped so it is neither unreadably narrow nor wider than needed.
const (
	timelineMin = 10
	timelineMax = 100
	// reserved leaves room for the left margin, the name/timeline gap, and the
	// trailing duration column when sizing the timeline.
	timelineReserved = 18
)

// row pairs a span's computed bar with the data needed to render it and to drill
// into the inspector. collapsed/hasChildren drive the expand caret.
type row struct {
	bar         render.Bar
	span        model.Span
	hasChildren bool
	collapsed   bool
}

// waterfallModel is Screen 2: a scrollable, collapsible proportional waterfall.
// Bar geometry (allBars) is computed once per width so collapsing a subtree never
// rescales the timeline; only the visible row set changes.
type waterfallModel struct {
	trace     model.Trace
	tree      *model.TreeNode
	spanByID  map[string]model.Span
	allBars   map[string]render.Bar
	collapsed map[string]bool
	visible   []row
	cursor    int
	nameCol   int

	vp     viewport.Model
	width  int
	height int
}

// newWaterfallModel assembles the tree, computes geometry, and primes the
// viewport. width/height fall back to a sane default until the first
// WindowSizeMsg arrives.
func newWaterfallModel(tr model.Trace, w, h int) waterfallModel {
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	m := waterfallModel{
		trace:     tr,
		tree:      model.AssembleTree(tr.Spans),
		spanByID:  make(map[string]model.Span, len(tr.Spans)),
		collapsed: make(map[string]bool),
		width:     w,
		height:    h,
		vp:        viewport.New(w, max(1, h-waterfallChrome)),
	}
	for _, s := range tr.Spans {
		m.spanByID[s.SpanID] = s
	}
	m.recomputeGeometry()
	m.recomputeVisible()
	m.refresh()
	return m
}

func (m waterfallModel) setSize(w, h int) waterfallModel {
	m.width, m.height = w, h
	if m.tree == nil {
		return m // nothing loaded yet; geometry is recomputed at load time
	}
	m.vp.Width = w
	m.vp.Height = max(1, h-waterfallChrome)
	m.recomputeGeometry()
	m.recomputeVisible()
	m.refresh()
	return m
}

// Update handles cursor movement, expand/collapse, and drill-in. Unhandled keys
// fall through to the viewport (page-up/down, etc.).
func (m waterfallModel) Update(msg tea.Msg) (waterfallModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.refresh()
			}
			return m, nil
		case "down", "j":
			if m.cursor < len(m.visible)-1 {
				m.cursor++
				m.refresh()
			}
			return m, nil
		case "home", "g":
			m.cursor = 0
			m.refresh()
			return m, nil
		case "end", "G":
			m.cursor = max(0, len(m.visible)-1)
			m.refresh()
			return m, nil
		case " ", "tab":
			m.toggleCollapse()
			return m, nil
		case "left", "h":
			m.setCollapsed(true)
			return m, nil
		case "right", "l":
			m.setCollapsed(false)
			return m, nil
		case "enter":
			if len(m.visible) == 0 {
				return m, nil
			}
			sp := m.visible[m.cursor].span
			return m, func() tea.Msg { return openInspectorMsg{span: sp} }
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m waterfallModel) View() string {
	label := m.trace.RunLabel
	if label == "" {
		label = m.trace.RootName
	}
	header := titleStyle.Render("grotto — "+render.CleanLabel(label, 60)) +
		dimStyle.Render(fmt.Sprintf("   %d spans · %s",
			m.trace.SpanCount, render.FormatDuration(m.trace.DurationNs)))
	help := helpStyle.Render("  ↑/↓ move · space collapse · enter inspect · esc back · q quit")
	return strings.Join([]string{header, m.vp.View(), help}, "\n")
}

// toggleCollapse flips the collapse state of the span under the cursor, if it has
// children. setCollapsed forces a specific state (for left/right keys).
func (m *waterfallModel) toggleCollapse() {
	if r, ok := m.current(); ok && r.hasChildren {
		m.collapsed[r.span.SpanID] = !m.collapsed[r.span.SpanID]
		m.recomputeVisible()
		m.refresh()
	}
}

func (m *waterfallModel) setCollapsed(v bool) {
	if r, ok := m.current(); ok && r.hasChildren && m.collapsed[r.span.SpanID] != v {
		m.collapsed[r.span.SpanID] = v
		m.recomputeVisible()
		m.refresh()
	}
}

func (m *waterfallModel) current() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return row{}, false
	}
	return m.visible[m.cursor], true
}

// recomputeGeometry computes the stable name-column width (over all spans) and
// the per-span bar geometry for the current terminal width. Bars are keyed by
// span ID so the visible-set walk can look them up.
func (m *waterfallModel) recomputeGeometry() {
	if m.tree == nil {
		m.allBars, m.nameCol = nil, 0
		return
	}
	nameCol := 0
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		// depth*2 indent + 2-cell caret + name width.
		if w := n.Depth*2 + 2 + lipgloss.Width(n.Span.Name); w > nameCol {
			nameCol = w
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(m.tree)
	m.nameCol = nameCol

	tw := m.width - nameCol - timelineReserved
	tw = min(timelineMax, max(timelineMin, tw))

	bars := render.Layout(m.tree, tw)
	m.allBars = make(map[string]render.Bar, len(bars))
	for _, b := range bars {
		m.allBars[b.SpanID] = b
	}
}

// recomputeVisible flattens the tree in pre-order, skipping the descendants of
// collapsed nodes, and clamps the cursor into the new range.
func (m *waterfallModel) recomputeVisible() {
	var rows []row
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		id := n.Span.SpanID
		rows = append(rows, row{
			bar:         m.allBars[id],
			span:        m.spanByID[id],
			hasChildren: len(n.Children) > 0,
			collapsed:   m.collapsed[id],
		})
		if m.collapsed[id] {
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	if m.tree != nil {
		walk(m.tree)
	}
	m.visible = rows
	if m.cursor >= len(rows) {
		m.cursor = max(0, len(rows)-1)
	}
}

// refresh re-renders the visible rows into the viewport and scrolls so the cursor
// stays in view.
func (m *waterfallModel) refresh() {
	m.vp.SetContent(m.renderContent())
	switch {
	case m.cursor < m.vp.YOffset:
		m.vp.SetYOffset(m.cursor)
	case m.cursor >= m.vp.YOffset+m.vp.Height:
		m.vp.SetYOffset(m.cursor - m.vp.Height + 1)
	}
}

func (m waterfallModel) renderContent() string {
	if len(m.visible) == 0 {
		return dimStyle.Render("  (empty trace)")
	}
	lines := make([]string, len(m.visible))
	for i, r := range m.visible {
		lines[i] = m.renderRow(r, i == m.cursor)
	}
	return strings.Join(lines, "\n")
}

func (m waterfallModel) renderRow(r row, selected bool) string {
	caret := "  "
	if r.hasChildren {
		if r.collapsed {
			caret = "▸ "
		} else {
			caret = "▾ "
		}
	}
	name := strings.Repeat("  ", r.bar.Depth) + caret + r.bar.Name
	pad := max(0, m.nameCol-lipgloss.Width(name))
	timeline := strings.Repeat(" ", r.bar.Offset) + strings.Repeat(barRune, r.bar.Width)
	marker := ""
	if r.span.Status == model.StatusError {
		marker = " !"
	}
	line := fmt.Sprintf("  %s%s  %s  %s%s",
		name, strings.Repeat(" ", pad), timeline,
		render.FormatDuration(r.bar.DurationNs), marker)
	if selected {
		return selectedStyle.Render(line)
	}
	return line
}
