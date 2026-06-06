package render

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// CriticalPath is the longest-duration chain through a build's dependency DAG —
// the sequence of compilation units that sets the build's minimum wall-clock
// time even with unlimited parallelism. Spans run from the head of the chain to
// its tail (each unblocks the next).
type CriticalPath struct {
	Spans       []model.Span // ordered head → tail
	CriticalNs  int64        // total duration along the path (the build's floor)
	TotalWorkNs int64        // sum of every unit's duration
	UnitCount   int          // number of units (DAG nodes)
}

// ComputeCriticalPath reconstructs the cargo dependency DAG from the cargo.unit /
// cargo.unblocks span attributes (stamped by the cargo adapter) and returns the
// longest-duration path through it. ok is false when the trace carries no such
// edges — a marks or OTLP trace — so callers degrade gracefully.
//
// Each unit's earliest finish is its own duration plus the latest finish among
// its predecessors; the critical path ends at the latest-finishing unit and is
// recovered by walking those predecessor links back to a source.
func ComputeCriticalPath(tr model.Trace) (CriticalPath, bool) {
	spanByUnit := make(map[int]model.Span)
	unblocks := make(map[int][]int)
	for _, s := range tr.Spans {
		idx, ok := unitIndex(s)
		if !ok {
			continue
		}
		spanByUnit[idx] = s
		unblocks[idx] = unitUnblocks(s)
	}
	if len(spanByUnit) == 0 {
		return CriticalPath{}, false
	}

	// Invert the forward edges (i unblocks j) into predecessor edges (i precedes
	// j), keeping only edges whose endpoints are present in this trace.
	preds := make(map[int][]int)
	for i, outs := range unblocks {
		for _, j := range outs {
			if _, ok := spanByUnit[j]; ok {
				preds[j] = append(preds[j], i)
			}
		}
	}

	// A unit cannot start before all its predecessors finished, so processing in
	// start-time order is a valid topological order for the longest-path DP.
	order := make([]int, 0, len(spanByUnit))
	for i := range spanByUnit {
		order = append(order, i)
	}
	sort.Slice(order, func(a, b int) bool {
		return spanByUnit[order[a]].StartedNs < spanByUnit[order[b]].StartedNs
	})

	finish := make(map[int]int64, len(order))
	back := make(map[int]int, len(order))
	var totalWork int64
	end := -1
	for _, i := range order {
		dur := spanByUnit[i].DurationNs
		totalWork += dur

		// best starts below zero so even a zero-duration predecessor is chosen,
		// keeping the chain intact; reset to 0 when the unit has no predecessor.
		best := int64(-1)
		bp := -1
		for _, p := range preds[i] {
			if finish[p] > best {
				best = finish[p]
				bp = p
			}
		}
		if best < 0 {
			best = 0
		}
		finish[i] = best + dur
		back[i] = bp
		if end == -1 || finish[i] > finish[end] {
			end = i
		}
	}

	// Walk predecessors back from the latest-finishing unit, then reverse.
	var rev []model.Span
	for cur := end; cur != -1; cur = back[cur] {
		rev = append(rev, spanByUnit[cur])
	}
	spans := make([]model.Span, len(rev))
	for i := range rev {
		spans[i] = rev[len(rev)-1-i]
	}

	return CriticalPath{
		Spans:       spans,
		CriticalNs:  finish[end],
		TotalWorkNs: totalWork,
		UnitCount:   len(spanByUnit),
	}, true
}

// unitIndex returns the cargo unit index recorded on a span, or false when the
// span carries no cargo.unit attribute.
func unitIndex(s model.Span) (int, bool) {
	for _, a := range s.Attributes {
		if a.Key == "cargo.unit" {
			i, err := strconv.Atoi(a.Value)
			if err != nil {
				return 0, false
			}
			return i, true
		}
	}
	return 0, false
}

// unitUnblocks returns the unit indices a span unblocks, parsed from its
// comma-separated cargo.unblocks attribute (absent when the unit unblocks none).
func unitUnblocks(s model.Span) []int {
	for _, a := range s.Attributes {
		if a.Key == "cargo.unblocks" {
			parts := strings.Split(a.Value, ",")
			out := make([]int, 0, len(parts))
			for _, p := range parts {
				if i, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
					out = append(out, i)
				}
			}
			return out
		}
	}
	return nil
}

// WriteCriticalPath renders the critical path of trace tr to w: a header naming
// the build's floor against its total work and wall-clock, then one row per unit
// on the path with a bar sized to its share of the chain. A trace without
// dependency edges renders a single explanatory line rather than failing.
func WriteCriticalPath(w io.Writer, tr model.Trace) error {
	cp, ok := ComputeCriticalPath(tr)
	if !ok {
		_, err := io.WriteString(w,
			"no dependency edges in this trace — only --adapter=cargo traces support --critical-path\n")
		return err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "critical path  %s  (the build's floor)\n", FormatDuration(cp.CriticalNs))
	fmt.Fprintf(&sb, "%s total compile work · %s wall-clock · %d units, %d on the path\n\n",
		FormatDuration(cp.TotalWorkNs), FormatDuration(tr.DurationNs), cp.UnitCount, len(cp.Spans))

	nameCol := 0
	for _, s := range cp.Spans {
		if len(s.Name) > nameCol {
			nameCol = len(s.Name)
		}
	}
	for _, s := range cp.Spans {
		width := 1
		if cp.CriticalNs > 0 {
			if w := int(s.DurationNs * int64(DefaultWidth) / cp.CriticalNs); w > width {
				width = w
			}
		}
		pad := strings.Repeat(" ", nameCol-len(s.Name))
		fmt.Fprintf(&sb, "  %s%s  %s  %s\n", s.Name, pad, strings.Repeat(barRune, width), FormatDuration(s.DurationNs))
	}

	_, err := io.WriteString(w, sb.String())
	return err
}
