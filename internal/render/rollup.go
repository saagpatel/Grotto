package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// DefaultMaxRows caps how many real (non-gap) child rows a single parent shows
// before the long tail is collapsed into a bucket. A cargo build can produce
// ~700 direct crate children; without a cap every parent with many parallelised
// units becomes an unreadable wall. 25 keeps the dominant contributors visible
// while still fitting comfortably in a terminal without scrolling.
const DefaultMaxRows = 25

// RollupExcess collapses the long tail of a parent's children into a single
// "(+N more)" bucket whenever a parent has more than maxRows real (non-gap)
// children, so a build with hundreds of parallel units stays legible. The
// bucket is a synthetic leaf — like gap nodes it is render-only: the stored
// trace is never touched. Mirrors InsertGaps in structure: recurse first so
// inner nodes are already rolled up before the outer node is evaluated, then
// rewrite the current node's children. Returns root for call chaining. No-op
// on nil root or a non-positive maxRows.
func RollupExcess(root *model.TreeNode, maxRows int) *model.TreeNode {
	if root == nil || maxRows <= 0 {
		return root
	}
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		if len(n.Children) == 0 {
			return
		}
		// Recurse before rewriting so inner collapsed nodes look like compact
		// leaves when we count children here, and buckets are never descended
		// into (they have no children of their own).
		for _, c := range n.Children {
			walk(c)
		}

		// Separate gap nodes from real spans. Gap nodes are always preserved
		// and never counted against the cap — they carry important timing
		// context that must survive the row reduction.
		var gaps, real []*model.TreeNode
		for _, c := range n.Children {
			if strings.HasPrefix(c.Span.SpanID, "gap:") {
				gaps = append(gaps, c)
			} else {
				real = append(real, c)
			}
		}

		if len(real) <= maxRows {
			// Under the cap — leave this parent's children byte-identical so
			// small traces are completely unaffected by the rollup pass.
			return
		}

		// Sort real children longest-first to identify the dominant units.
		sort.SliceStable(real, func(i, j int) bool {
			return real[i].Span.DurationNs > real[j].Span.DurationNs
		})

		keep := real[:maxRows-1]     // dominant children that get their own row
		collapse := real[maxRows-1:] // long-tail units that become one bucket row

		// collapse is guaranteed to have >= 2 elements:
		//   len(real) > maxRows  =>  len(real) >= maxRows+1
		//   len(collapse) = len(real) - (maxRows-1) >= 2.

		bucket := newRollupBucket(n, collapse)

		// Merge gaps + kept + bucket, then re-sort by start time so the
		// timeline still reads left to right even though we sorted by duration
		// above. A stable sort preserves the existing relative ordering among
		// nodes with identical timestamps.
		merged := make([]*model.TreeNode, 0, len(gaps)+len(keep)+1)
		merged = append(merged, gaps...)
		merged = append(merged, keep...)
		merged = append(merged, bucket)
		sort.SliceStable(merged, func(i, j int) bool {
			return merged[i].Span.StartedNs < merged[j].Span.StartedNs
		})
		n.Children = merged
	}
	walk(root)
	return root
}

// newRollupBucket builds the synthetic leaf that represents the collapsed tail.
// Its timeline bar spans the union interval [min(start), max(end)] so it sits
// in the right position on the waterfall. Its DurationNs is the SUM of all
// collapsed children's DurationNs — that number represents actual work, not the
// wall-clock width of the union (which can be wider when units overlapped or
// narrower than total work when units ran in parallel). The bar's visual extent
// therefore diverges from the numeric duration; a comment in the code notes
// this so future readers don't "fix" it back to end-start.
//
// Status propagates the worst-case outcome: if any collapsed child errored, the
// bucket is marked error so a failing unit cannot silently disappear from view.
func newRollupBucket(parent *model.TreeNode, collapse []*model.TreeNode) *model.TreeNode {
	minStart := collapse[0].Span.StartedNs
	maxEnd := collapse[0].Span.EndedNs
	var sumDur int64
	status := model.StatusOk
	for _, c := range collapse {
		if c.Span.StartedNs < minStart {
			minStart = c.Span.StartedNs
		}
		if c.Span.EndedNs > maxEnd {
			maxEnd = c.Span.EndedNs
		}
		sumDur += c.Span.DurationNs
		if c.Span.Status == model.StatusError {
			// A hidden failed unit must not vanish silently: surface the error
			// on the bucket even if most collapsed children were healthy.
			status = model.StatusError
		}
	}

	return &model.TreeNode{
		Span: model.Span{
			// rollup: prefix is a third namespace alongside the real 16-hex
			// span IDs and the gap: synthetic IDs — it never collides with either.
			SpanID: fmt.Sprintf("rollup:%s", parent.Span.SpanID),
			Name:   fmt.Sprintf("(+%d more)", len(collapse)),
			Kind:   model.KindInternal,
			Status: status,
			// StartedNs/EndedNs = union interval: positions the bar correctly on
			// the timeline even though the collapsed units may overlap each other.
			StartedNs: minStart,
			EndedNs:   maxEnd,
			// DurationNs = sum of constituent work, NOT end-start of the union.
			// The two diverge when units overlap (parallel builds). The number
			// shown to the user should represent total work time, not wall-clock
			// extent of the union; the bar already conveys the latter.
			DurationNs: sumDur,
		},
		Depth: parent.Depth + 1,
		// Children intentionally left nil: the bucket is a synthetic leaf.
		// RollupExcess never recurses into it.
	}
}
