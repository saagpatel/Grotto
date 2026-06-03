package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/render"
)

var labelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))

// inspectorModel is Screen 3: focused detail for a single span — its identity,
// timing, kind/status, and typed attributes. It is a static detail view in v1;
// navigation (back/quit) is owned by the root.
type inspectorModel struct {
	span   model.Span
	width  int
	height int
}

func newInspectorModel(sp model.Span, w, h int) inspectorModel {
	return inspectorModel{span: sp, width: w, height: h}
}

func (m inspectorModel) setSize(w, h int) inspectorModel {
	m.width, m.height = w, h
	return m
}

func (m inspectorModel) Update(_ tea.Msg) (inspectorModel, tea.Cmd) {
	return m, nil
}

func (m inspectorModel) View() string {
	sp := m.span
	lines := []string{titleStyle.Render("span detail"), ""}

	field := func(k, v string) string {
		return "  " + labelStyle.Render(fmt.Sprintf("%-12s", k)) + v
	}

	parent := sp.ParentSpanID
	if parent == "" {
		parent = dimStyle.Render("(root)")
	}
	status := statusLabel(sp.Status)
	if sp.Status == model.StatusError {
		status = errStyle.Render(status)
	}

	lines = append(lines,
		field("name", sp.Name),
		field("span id", sp.SpanID),
		field("parent", parent),
		field("kind", kindLabel(sp.Kind)),
		field("status", status),
		field("duration", render.FormatDuration(sp.DurationNs)),
		field("started", fmt.Sprintf("%d ns", sp.StartedNs)),
		field("ended", fmt.Sprintf("%d ns", sp.EndedNs)),
		"",
		labelStyle.Render("  attributes"))

	if len(sp.Attributes) == 0 {
		lines = append(lines, dimStyle.Render("    (none)"))
	} else {
		for _, a := range sp.Attributes {
			lines = append(lines, fmt.Sprintf("    %-24s %s %s",
				a.Key, dimStyle.Render("("+a.ValueType+")"), a.Value))
		}
	}

	lines = append(lines, "", helpStyle.Render("  esc back · q quit"))
	return strings.Join(lines, "\n")
}

// kindLabel and statusLabel map the OTel enums to lowercase display strings.
func kindLabel(k model.SpanKind) string {
	switch k {
	case model.KindInternal:
		return "internal"
	case model.KindServer:
		return "server"
	case model.KindClient:
		return "client"
	case model.KindProducer:
		return "producer"
	case model.KindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

func statusLabel(s model.StatusCode) string {
	switch s {
	case model.StatusOk:
		return "ok"
	case model.StatusError:
		return "error"
	default:
		return "unset"
	}
}
