package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/saagpatel/grotto/internal/model"
)

// rollupNode builds a TreeNode for rollup tests. start/end are nanosecond
// timestamps; DurationNs is computed from them. name doubles as the span ID
// unless the caller needs a specific ID (e.g. a gap: node — use rollupNodeID).
func rollupNode(name string, start, end int64, depth int, children ...*model.TreeNode) *model.TreeNode {
	return rollupNodeID(name, name, start, end, depth, model.StatusOk, children...)
}

// rollupNodeID is like rollupNode but lets the caller set SpanID and Status
// independently, needed for gap: nodes and error-status tests.
func rollupNodeID(id, name string, start, end int64, depth int, status model.StatusCode, children ...*model.TreeNode) *model.TreeNode {
	return &model.TreeNode{
		Span: model.Span{
			SpanID:     id,
			Name:       name,
			Kind:       model.KindInternal,
			Status:     status,
			StartedNs:  start,
			EndedNs:    end,
			DurationNs: end - start,
		},
		Depth:    depth,
		Children: children,
	}
}

// buildChildren constructs n sibling leaf nodes under a root. Each child has a
// distinct start time (i*1000), a duration given by the durs slice, and depth 1.
// durs must be length n; any element <= 0 is clamped to 1 to keep EndedNs > StartedNs.
func buildChildren(n int, durs []int64) []*model.TreeNode {
	nodes := make([]*model.TreeNode, n)
	for i := range nodes {
		start := int64(i) * 10_000
		dur := durs[i]
		if dur <= 0 {
			dur = 1
		}
		nodes[i] = rollupNode(fmt.Sprintf("child-%d", i), start, start+dur, 1)
	}
	return nodes
}

// childIDs returns the ordered SpanIDs of n's immediate children.
func childIDs(n *model.TreeNode) []string {
	ids := make([]string, len(n.Children))
	for i, c := range n.Children {
		ids[i] = c.Span.SpanID
	}
	return ids
}

// childCount returns len(n.Children).
func childCount(n *model.TreeNode) int { return len(n.Children) }

// findBucket returns the first child of n whose SpanID starts with "rollup:".
// Returns nil when none is found.
func findBucket(n *model.TreeNode) *model.TreeNode {
	for _, c := range n.Children {
		if strings.HasPrefix(c.Span.SpanID, "rollup:") {
			return c
		}
	}
	return nil
}

// TestRollupExcess_NoOpUnderCap: a root with 10 real children stays unchanged.
func TestRollupExcess_NoOpUnderCap(t *testing.T) {
	durs := make([]int64, 10)
	for i := range durs {
		durs[i] = int64(i+1) * 1000
	}
	children := buildChildren(10, durs)
	root := rollupNode("root", 0, 200_000, 0, children...)
	wantIDs := childIDs(root)

	RollupExcess(root, DefaultMaxRows)

	if childCount(root) != 10 {
		t.Fatalf("child count = %d, want 10 (no-op under cap)", childCount(root))
	}
	for i, id := range childIDs(root) {
		if id != wantIDs[i] {
			t.Errorf("child[%d].SpanID = %q, want %q", i, id, wantIDs[i])
		}
	}
	if findBucket(root) != nil {
		t.Error("bucket appeared on an under-cap parent")
	}
}

// TestRollupExcess_CollapseOverCap: 100 children → exactly DefaultMaxRows rows;
// bucket name is "(+76 more)"; the 24 kept are the 24 largest by duration.
func TestRollupExcess_CollapseOverCap(t *testing.T) {
	n := 100
	durs := make([]int64, n)
	for i := range durs {
		durs[i] = int64(i+1) * 1000 // child-0 = 1000ns ... child-99 = 100000ns
	}
	children := buildChildren(n, durs)
	root := rollupNode("root", 0, int64(n)*10_000+100_001, 0, children...)
	RollupExcess(root, DefaultMaxRows)

	if childCount(root) != DefaultMaxRows {
		t.Fatalf("child count = %d, want %d (24 kept + 1 bucket)", childCount(root), DefaultMaxRows)
	}

	bucket := findBucket(root)
	if bucket == nil {
		t.Fatal("no rollup bucket found")
	}
	wantName := fmt.Sprintf("(+%d more)", 100-(DefaultMaxRows-1))
	if bucket.Span.Name != wantName {
		t.Errorf("bucket name = %q, want %q", bucket.Span.Name, wantName)
	}

	// The 24 kept should be the 24 largest by duration (child-76..child-99,
	// i.e. durs 77000..100000ns). Sum of collapsed = sum(durs[0..75]) = sum(1..76)*1000.
	var wantSum int64
	for i := 0; i < 76; i++ {
		wantSum += durs[i]
	}
	if bucket.Span.DurationNs != wantSum {
		t.Errorf("bucket DurationNs = %d, want %d (sum of 76 collapsed durations)", bucket.Span.DurationNs, wantSum)
	}
}

// TestRollupExcess_BucketInterval: bucket StartedNs/EndedNs == min/max over collapsed set.
func TestRollupExcess_BucketInterval(t *testing.T) {
	// Build DefaultMaxRows+2 children (27) so 3 are collapsed (keep=maxRows-1=24,
	// collapse=27-24=3). The 3 smallest-duration ones are child-0/1/2 with:
	//   child-0: start=0,     dur=1000,  end=1000
	//   child-1: start=10000, dur=2000,  end=12000
	//   child-2: start=20000, dur=3000,  end=23000
	// Union: minStart=0, maxEnd=23000.
	n := DefaultMaxRows + 2
	durs := make([]int64, n)
	for i := range durs {
		durs[i] = int64(i+1) * 1000
	}
	children := buildChildren(n, durs)
	root := rollupNode("root", 0, int64(n)*10_000+int64(n)*1000+1, 0, children...)
	RollupExcess(root, DefaultMaxRows)

	bucket := findBucket(root)
	if bucket == nil {
		t.Fatal("no rollup bucket found")
	}

	// keep = maxRows-1 = 24; collapse = 27-24 = 3 (the 3 smallest by duration).
	collapseCount := n - (DefaultMaxRows - 1)
	// Compute expected union over the collapsed set.
	// buildChildren assigns start=i*10000, end=start+durs[i].
	wantStart := children[0].Span.StartedNs
	wantEnd := children[0].Span.EndedNs
	for i := 1; i < collapseCount; i++ {
		if children[i].Span.StartedNs < wantStart {
			wantStart = children[i].Span.StartedNs
		}
		if children[i].Span.EndedNs > wantEnd {
			wantEnd = children[i].Span.EndedNs
		}
	}

	if bucket.Span.StartedNs != wantStart {
		t.Errorf("bucket StartedNs = %d, want %d", bucket.Span.StartedNs, wantStart)
	}
	if bucket.Span.EndedNs != wantEnd {
		t.Errorf("bucket EndedNs = %d, want %d", bucket.Span.EndedNs, wantEnd)
	}
}

// TestRollupExcess_ErrorPropagation: a failed collapsed child surfaces on the bucket.
func TestRollupExcess_ErrorPropagation(t *testing.T) {
	t.Run("one errored collapsed child → bucket StatusError", func(t *testing.T) {
		children := buildChildren(DefaultMaxRows+1, makeEqualDurs(DefaultMaxRows+1, 1000))
		// Mark the first child (will be collapsed: it has the smallest duration
		// when all durations are equal — stable sort keeps it first but at
		// index [DefaultMaxRows] in the keep-vs-collapse split when all equal;
		// to be safe, give it a uniquely small duration so it's definitely collapsed).
		children[0].Span.DurationNs = 1
		children[0].Span.EndedNs = children[0].Span.StartedNs + 1
		children[0].Span.Status = model.StatusError

		root := rollupNode("root", 0, 999_999, 0, children...)
		RollupExcess(root, DefaultMaxRows)

		bucket := findBucket(root)
		if bucket == nil {
			t.Fatal("no rollup bucket")
		}
		if bucket.Span.Status != model.StatusError {
			t.Errorf("bucket Status = %v, want StatusError", bucket.Span.Status)
		}
	})

	t.Run("no errored collapsed children → bucket StatusOk", func(t *testing.T) {
		children := buildChildren(DefaultMaxRows+1, makeEqualDurs(DefaultMaxRows+1, 1000))
		// Give the first child the smallest duration so it is definitely collapsed,
		// but leave it StatusOk.
		children[0].Span.DurationNs = 1
		children[0].Span.EndedNs = children[0].Span.StartedNs + 1

		root := rollupNode("root", 0, 999_999, 0, children...)
		RollupExcess(root, DefaultMaxRows)

		bucket := findBucket(root)
		if bucket == nil {
			t.Fatal("no rollup bucket")
		}
		if bucket.Span.Status != model.StatusOk {
			t.Errorf("bucket Status = %v, want StatusOk", bucket.Span.Status)
		}
	})
}

// TestRollupExcess_GapPreservation: a gap: node mixed in with over-cap real
// children survives untouched and is not counted toward the cap.
func TestRollupExcess_GapPreservation(t *testing.T) {
	// DefaultMaxRows real children + 1 extra (so 1 will be collapsed) + 1 gap node.
	realN := DefaultMaxRows + 1
	durs := make([]int64, realN)
	for i := range durs {
		durs[i] = int64(i+1) * 1000
	}
	realChildren := buildChildren(realN, durs)

	gapNode := rollupNodeID(
		"gap:root:0", gapName,
		0, 500, 1, model.StatusOk,
	)

	allChildren := append([]*model.TreeNode{gapNode}, realChildren...)
	root := rollupNode("root", 0, 999_999, 0, allChildren...)
	RollupExcess(root, DefaultMaxRows)

	// The gap node must still be present.
	gapFound := false
	for _, c := range root.Children {
		if c.Span.SpanID == "gap:root:0" {
			gapFound = true
			break
		}
	}
	if !gapFound {
		t.Error("gap node was removed during rollup")
	}

	// Real children: DefaultMaxRows real nodes produced DefaultMaxRows-1 kept + 1 bucket.
	// Total children = 1 gap + (DefaultMaxRows-1) kept + 1 bucket = DefaultMaxRows+1.
	want := DefaultMaxRows + 1
	if childCount(root) != want {
		t.Errorf("child count = %d, want %d (gap + kept + bucket)", childCount(root), want)
	}
}

// TestRollupExcess_StartSorted: children are ordered by StartedNs after rollup.
func TestRollupExcess_StartSorted(t *testing.T) {
	// Build over-cap children with distinct start times.
	n := DefaultMaxRows + 5
	durs := make([]int64, n)
	for i := range durs {
		durs[i] = int64(i+1) * 1000
	}
	children := buildChildren(n, durs)
	root := rollupNode("root", 0, 999_999, 0, children...)
	RollupExcess(root, DefaultMaxRows)

	for i := 1; i < len(root.Children); i++ {
		prev := root.Children[i-1].Span.StartedNs
		curr := root.Children[i].Span.StartedNs
		if curr < prev {
			t.Errorf("children[%d].StartedNs (%d) < children[%d].StartedNs (%d): not sorted by start time",
				i, curr, i-1, prev)
		}
	}
}

// TestRollupExcess_NilNoOp and TestRollupExcess_ZeroMaxRowsNoOp guard the early-return paths.
func TestRollupExcess_NilNoOp(t *testing.T) {
	if got := RollupExcess(nil, DefaultMaxRows); got != nil {
		t.Fatalf("RollupExcess(nil) = %v, want nil", got)
	}
}

func TestRollupExcess_ZeroMaxRowsNoOp(t *testing.T) {
	root := rollupNode("root", 0, 1000, 0, rollupNode("a", 0, 500, 1), rollupNode("b", 500, 1000, 1))
	RollupExcess(root, 0)
	if childCount(root) != 2 {
		t.Errorf("child count = %d, want 2 (no-op on maxRows=0)", childCount(root))
	}
}

// makeEqualDurs returns a slice of n copies of dur.
func makeEqualDurs(n int, dur int64) []int64 {
	s := make([]int64, n)
	for i := range s {
		s[i] = dur
	}
	return s
}
