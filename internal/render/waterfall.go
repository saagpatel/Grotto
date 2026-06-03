package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/saagpatel/grotto/internal/model"
)

// barRune is the filled timeline character.
const barRune = "█"

// WriteWaterfall renders trace tr as an indented, proportional-bar waterfall to
// w. Spans are shown in pre-order; each bar is positioned and sized relative to
// the root span's duration. An empty trace renders a single placeholder line.
func WriteWaterfall(w io.Writer, tr model.Trace) error {
	root := model.AssembleTree(tr.Spans)
	if root == nil {
		_, err := io.WriteString(w, "(empty trace)\n")
		return err
	}
	bars := Layout(root, DefaultWidth)

	nameCol := 0
	for _, b := range bars {
		if l := b.Depth*2 + len(b.Name); l > nameCol {
			nameCol = l
		}
	}

	var sb strings.Builder
	for _, b := range bars {
		label := strings.Repeat("  ", b.Depth) + b.Name
		pad := strings.Repeat(" ", nameCol-len(label))
		timeline := strings.Repeat(" ", b.Offset) + strings.Repeat(barRune, b.Width)
		marker := ""
		if b.Status == model.StatusError {
			marker = " !"
		}
		fmt.Fprintf(&sb, "%s%s  %s  %s%s\n", label, pad, timeline, FormatDuration(b.DurationNs), marker)
	}

	_, err := io.WriteString(w, sb.String())
	return err
}

// WriteJSON writes trace tr as indented JSON to w.
func WriteJSON(w io.Writer, tr model.Trace) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tr); err != nil {
		return fmt.Errorf("encode trace json: %w", err)
	}
	return nil
}

// FormatDuration renders a nanosecond duration in the largest readable unit.
// Exported so the TUI run list and `grotto list` render durations identically to
// the static waterfall.
func FormatDuration(ns int64) string {
	d := time.Duration(ns)
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d >= time.Microsecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	default:
		return fmt.Sprintf("%dns", ns)
	}
}

// HumanAge renders the elapsed time since a nanosecond wall-clock timestamp in
// the largest coarse unit (s/m/h/d). Future timestamps (clock skew) read as
// "0s ago". Shared by the TUI run list and `grotto list`.
func HumanAge(createdNs int64, now time.Time) string {
	d := now.Sub(time.Unix(0, createdNs))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
