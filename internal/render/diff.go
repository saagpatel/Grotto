package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// DeltaKind classifies a span in a two-trace comparison.
type DeltaKind int

const (
	DeltaMatched DeltaKind = iota // present in both traces
	DeltaAdded                    // only in B (the second trace)
	DeltaRemoved                  // only in A (the first trace)
)

// SpanDelta is one span's comparison across two traces. For a matched span both
// durations are set and DeltaNs = B − A; for added/removed only the present
// side's duration is set.
type SpanDelta struct {
	Name    string
	Depth   int
	Kind    DeltaKind
	ANs     int64
	BNs     int64
	DeltaNs int64
}

// spanKey identifies a span for cross-trace matching: same name at the same
// depth, disambiguated by occurrence index among (name, depth)-equal spans in
// pre-order. This tolerates unrelated sibling reordering while still telling
// repeated identically-named steps apart.
type spanKey struct {
	name  string
	depth int
	index int
}

type nameDepth struct {
	name  string
	depth int
}

// flattenKeyed walks the tree in pre-order, assigning each span a spanKey and
// recording its duration. The returned slice preserves pre-order.
func flattenKeyed(root *model.TreeNode) ([]spanKey, map[spanKey]int64) {
	occ := map[nameDepth]int{}
	var keys []spanKey
	dur := map[spanKey]int64{}
	var walk func(n *model.TreeNode)
	walk = func(n *model.TreeNode) {
		nd := nameDepth{n.Span.Name, n.Depth}
		k := spanKey{name: n.Span.Name, depth: n.Depth, index: occ[nd]}
		occ[nd]++
		keys = append(keys, k)
		dur[k] = n.Span.EndedNs - n.Span.StartedNs
		for _, c := range n.Children {
			walk(c)
		}
	}
	if root != nil {
		walk(root)
	}
	return keys, dur
}

// Diff compares two traces span-by-span. Matched and removed spans appear in A's
// pre-order; spans only in B (added) follow in B's pre-order. Returns nil when
// both traces are empty.
//
// Spans are paired by (name, depth, occurrence-index), where the index counts
// equal (name, depth) pairs in pre-order. Limitation: AssembleTree orders
// siblings by start time, so if two identically-named same-depth siblings swap
// start order between runs, they pair by position and report a delta against the
// other's duration. Distinctly-named or structurally-stable spans are unaffected.
func Diff(a, b model.Trace) []SpanDelta {
	aKeys, aDur := flattenKeyed(model.AssembleTree(a.Spans))
	bKeys, bDur := flattenKeyed(model.AssembleTree(b.Spans))
	matched := make(map[spanKey]bool, len(bKeys))

	var out []SpanDelta
	for _, k := range aKeys {
		if bd, ok := bDur[k]; ok {
			matched[k] = true
			out = append(out, SpanDelta{
				Name: k.name, Depth: k.depth, Kind: DeltaMatched,
				ANs: aDur[k], BNs: bd, DeltaNs: bd - aDur[k],
			})
			continue
		}
		out = append(out, SpanDelta{Name: k.name, Depth: k.depth, Kind: DeltaRemoved, ANs: aDur[k]})
	}
	for _, k := range bKeys {
		if !matched[k] {
			out = append(out, SpanDelta{Name: k.name, Depth: k.depth, Kind: DeltaAdded, BNs: bDur[k]})
		}
	}
	return out
}

// deltaImpact is the magnitude of a span's change, used to rank deltas by how
// much they moved: the absolute duration delta for a matched span, or the full
// present-side duration for a span that appeared (added) or vanished (removed).
func deltaImpact(d SpanDelta) int64 {
	switch d.Kind {
	case DeltaAdded:
		return d.BNs
	case DeltaRemoved:
		return d.ANs
	default:
		if d.DeltaNs < 0 {
			return -d.DeltaNs
		}
		return d.DeltaNs
	}
}

// SortByImpact reorders deltas in place by descending change magnitude, so the
// biggest movers surface first — e.g. the crates a warm cache saved the most
// time on. The sort is stable, so equal-impact spans keep their structural
// (pre-order) relationship. This trades the parent/child indentation reading for
// impact ranking, which is why it is opt-in (`grotto diff --sort=delta`).
func SortByImpact(deltas []SpanDelta) {
	sort.SliceStable(deltas, func(i, j int) bool {
		return deltaImpact(deltas[i]) > deltaImpact(deltas[j])
	})
}

// WriteDiff renders a comparison of traces a and b to w: a header with the
// run labels and total-duration delta, then one line per span. Matched spans
// show "A → B  ±delta"; added/removed spans are prefixed with + / −.
func WriteDiff(w io.Writer, a, b model.Trace, deltas []SpanDelta) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff  %s %s → %s %s\n",
		shortTraceID(a.TraceID), CleanLabel(a.RunLabel, labelColWidth),
		shortTraceID(b.TraceID), CleanLabel(b.RunLabel, labelColWidth))
	fmt.Fprintf(&sb, "total %s → %s  (%s)\n\n",
		FormatDuration(a.DurationNs), FormatDuration(b.DurationNs),
		signedDuration(b.DurationNs-a.DurationNs))

	for _, d := range deltas {
		indent := strings.Repeat("  ", d.Depth)
		switch d.Kind {
		case DeltaMatched:
			fmt.Fprintf(&sb, "  %s%s  %s → %s  %s\n",
				indent, d.Name, FormatDuration(d.ANs), FormatDuration(d.BNs), signedDuration(d.DeltaNs))
		case DeltaAdded:
			fmt.Fprintf(&sb, "+ %s%s  %s\n", indent, d.Name, FormatDuration(d.BNs))
		case DeltaRemoved:
			fmt.Fprintf(&sb, "- %s%s  %s\n", indent, d.Name, FormatDuration(d.ANs))
		}
	}

	_, err := io.WriteString(w, sb.String())
	return err
}

// shortTraceID returns the leading 8 hex chars of a trace ID — enough to keep
// two traces distinguishable in the diff header (whose labels are truncated and
// may share a long prefix) without printing the full 32-char key.
func shortTraceID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// signedDuration formats a nanosecond delta with an explicit sign; zero renders
// as a bare "0".
func signedDuration(ns int64) string {
	switch {
	case ns > 0:
		return "+" + FormatDuration(ns)
	case ns < 0:
		return "-" + FormatDuration(-ns)
	default:
		return "0"
	}
}
