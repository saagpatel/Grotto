// Package render turns a trace's span tree into output: a proportional-bar
// waterfall (shared by `grotto show` and, later, the TUI) and a JSON dump. The
// layout math is a pure function so it can be unit-tested independently of any
// terminal or I/O.
package render

import (
	"math"

	"github.com/saagpatel/grotto/internal/model"
)

// DefaultWidth is the character width of the timeline column.
const DefaultWidth = 40

// Bar is one span's position in the waterfall: its pre-order row, depth for
// indentation, and the offset/width of its proportional timeline bar (in
// characters, relative to the root span's full duration).
type Bar struct {
	SpanID     string
	Name       string
	Depth      int
	Offset     int
	Width      int
	DurationNs int64
	Status     model.StatusCode
}

// Layout flattens the tree in pre-order and computes a timeline bar for each
// span, scaled so the root span fills the full width. Returns nil for a nil tree
// or a non-positive width.
func Layout(root *model.TreeNode, width int) []Bar {
	if root == nil || width <= 0 {
		return nil
	}

	rootStart := root.Span.StartedNs
	rootDur := root.Span.EndedNs - root.Span.StartedNs

	var bars []Bar
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		offset, w := barGeometry(n.Span, rootStart, rootDur, width)
		bars = append(bars, Bar{
			SpanID:     n.Span.SpanID,
			Name:       n.Span.Name,
			Depth:      n.Depth,
			Offset:     offset,
			Width:      w,
			DurationNs: n.Span.EndedNs - n.Span.StartedNs,
			Status:     n.Span.Status,
		})
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return bars
}

// barGeometry computes the offset and width (in characters) of a span's bar
// within a width-character timeline. Width is at least 1 so even a zero-duration
// span is visible, and the bar is clamped to stay within the timeline.
func barGeometry(s model.Span, rootStart, rootDur int64, width int) (offset, w int) {
	if rootDur <= 0 {
		return 0, width
	}

	scale := float64(width) / float64(rootDur)
	offset = int(math.Round(float64(s.StartedNs-rootStart) * scale))
	w = int(math.Round(float64(s.EndedNs-s.StartedNs) * scale))

	if offset < 0 {
		offset = 0
	}
	if offset > width {
		offset = width
	}
	if w < 1 {
		w = 1
	}
	if offset+w > width {
		// Shrink the bar so it ends at the timeline edge; never below 1 char,
		// in which case nudge it into the last column (offset is width here).
		w = width - offset
		if w < 1 {
			w = 1
			offset = width - 1
		}
	}
	return offset, w
}
