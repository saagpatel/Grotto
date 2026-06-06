package render

import (
	"fmt"
	"sort"

	"github.com/saagpatel/grotto/internal/model"
)

// gapName labels a synthetic span standing in for unaccounted time — a stretch
// of a parent span's interval not covered by any of its marked children.
const gapName = "(gap)"

// InsertGaps augments the tree in place so the waterfall can show unaccounted
// time. Under every span that has children, it inserts synthetic leaf "(gap)"
// nodes for each sub-interval of the parent not covered by any child, provided
// the gap is at least minNs wide. Gaps are placed in start-time order among the
// real children, so the timeline reads left to right.
//
// This makes otherwise-invisible time legible: setup before the first mark, or
// an unmarked step inside a section (e.g. a `go vet` that runs before the first
// `--child` compile mark). The gap nodes are render-only — they are never
// stored — so the canonical span set is unchanged.
//
// Returns root for call chaining. A nil root or a non-positive minNs (gaps
// disabled) leaves the tree untouched.
func InsertGaps(root *model.TreeNode, minNs int64) *model.TreeNode {
	if root == nil || minNs <= 0 {
		return root
	}
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		if len(n.Children) == 0 {
			return
		}
		// Recurse before rewriting children so gaps stay leaves (we never
		// descend into a gap) and nested gaps are computed from real spans.
		for _, c := range n.Children {
			walk(c)
		}

		kids := append([]*model.TreeNode(nil), n.Children...)
		sort.SliceStable(kids, func(i, j int) bool {
			return kids[i].Span.StartedNs < kids[j].Span.StartedNs
		})

		merged := make([]*model.TreeNode, 0, len(kids)*2+1)
		cursor := n.Span.StartedNs
		gapIdx := 0
		addGap := func(from, to int64) {
			if to-from < minNs {
				return
			}
			merged = append(merged, newGapNode(n, gapIdx, from, to))
			gapIdx++
		}
		for _, c := range kids {
			if c.Span.StartedNs > cursor {
				addGap(cursor, c.Span.StartedNs)
			}
			merged = append(merged, c)
			if c.Span.EndedNs > cursor {
				cursor = c.Span.EndedNs
			}
		}
		addGap(cursor, n.Span.EndedNs)
		n.Children = merged
	}
	walk(root)
	return root
}

// newGapNode builds a synthetic leaf node for the interval [start, end] under
// parent. The span ID is deterministic and namespaced so it never collides with
// a real 16-hex span ID and stays stable across re-layouts.
func newGapNode(parent *model.TreeNode, idx int, start, end int64) *model.TreeNode {
	return &model.TreeNode{
		Span: model.Span{
			SpanID:     fmt.Sprintf("gap:%s:%d", parent.Span.SpanID, idx),
			Name:       gapName,
			Kind:       model.KindInternal,
			Status:     model.StatusOk,
			StartedNs:  start,
			EndedNs:    end,
			DurationNs: end - start,
		},
		Depth: parent.Depth + 1,
	}
}

// GapMinNs is the smallest unaccounted interval worth surfacing at a given
// timeline width: one character. A gap narrower than this would not render as a
// visible bar, so showing it would only add noise. A non-positive root duration
// or width disables gap insertion (returns 0).
func GapMinNs(rootDur int64, width int) int64 {
	if rootDur <= 0 || width <= 0 {
		return 0
	}
	if n := rootDur / int64(width); n > 0 {
		return n
	}
	return 1
}
