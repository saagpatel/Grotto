package render

import (
	"strings"
	"testing"

	"github.com/saagpatel/grotto/internal/model"
)

// gapTreeNode builds a TreeNode for gap tests: name doubles as the span ID.
func gapTreeNode(name string, start, end int64, depth int, children ...*model.TreeNode) *model.TreeNode {
	return &model.TreeNode{
		Span: model.Span{
			SpanID: name, Name: name,
			StartedNs: start, EndedNs: end, DurationNs: end - start,
		},
		Depth:    depth,
		Children: children,
	}
}

// childNames returns the ordered child names of n, for asserting structure.
func childNames(n *model.TreeNode) []string {
	names := make([]string, len(n.Children))
	for i, c := range n.Children {
		names[i] = c.Span.Name
	}
	return names
}

func TestInsertGaps(t *testing.T) {
	t.Run("nil root is a no-op", func(t *testing.T) {
		if got := InsertGaps(nil, 10); got != nil {
			t.Fatalf("InsertGaps(nil) = %v, want nil", got)
		}
	})

	t.Run("leaf with no children is untouched", func(t *testing.T) {
		root := gapTreeNode("leaf", 0, 1000, 0)
		InsertGaps(root, 10)
		if len(root.Children) != 0 {
			t.Errorf("leaf gained children: %v", childNames(root))
		}
	})

	t.Run("leading gap before the first child", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0, gapTreeNode("a", 200, 1000, 1))
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{gapName, "a"}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
		g := root.Children[0]
		if g.Span.StartedNs != 0 || g.Span.EndedNs != 200 || g.Span.DurationNs != 200 {
			t.Errorf("gap bounds = [%d,%d] dur %d, want [0,200] dur 200", g.Span.StartedNs, g.Span.EndedNs, g.Span.DurationNs)
		}
		if g.Depth != 1 {
			t.Errorf("gap depth = %d, want 1", g.Depth)
		}
		if len(g.Children) != 0 {
			t.Errorf("gap should be a leaf, has %d children", len(g.Children))
		}
	})

	t.Run("middle gap between two children", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0,
			gapTreeNode("a", 0, 300, 1),
			gapTreeNode("b", 600, 1000, 1),
		)
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{"a", gapName, "b"}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
		g := root.Children[1]
		if g.Span.StartedNs != 300 || g.Span.EndedNs != 600 {
			t.Errorf("gap bounds = [%d,%d], want [300,600]", g.Span.StartedNs, g.Span.EndedNs)
		}
	})

	t.Run("trailing gap after the last child", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0, gapTreeNode("a", 0, 400, 1))
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{"a", gapName}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
	})

	t.Run("sub-threshold gap is not inserted", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0, gapTreeNode("a", 5, 1000, 1))
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{"a"}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v (5ns gap is below the 10ns floor)", got, want)
		}
	})

	t.Run("fully-tiled parent gets no gaps", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0,
			gapTreeNode("a", 0, 500, 1),
			gapTreeNode("b", 500, 1000, 1),
		)
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{"a", "b"}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
	})

	t.Run("nested gap under a child that has its own children (the vet case)", func(t *testing.T) {
		build := gapTreeNode("build", 0, 2000, 1, gapTreeNode("compile", 1900, 2000, 2))
		root := gapTreeNode("root", 0, 2000, 0, build)
		InsertGaps(root, 10)
		// Root's single child fully covers it: no gap under root.
		if got, want := childNames(root), []string{"build"}; !equalStrings(got, want) {
			t.Fatalf("root children = %v, want %v", got, want)
		}
		// build has 1900ns of unmarked time before compile: a gap appears.
		if got, want := childNames(build), []string{gapName, "compile"}; !equalStrings(got, want) {
			t.Fatalf("build children = %v, want %v", got, want)
		}
		if g := build.Children[0]; g.Depth != 2 || g.Span.DurationNs != 1900 {
			t.Errorf("nested gap depth/dur = %d/%d, want 2/1900", g.Depth, g.Span.DurationNs)
		}
	})

	t.Run("overlapping children leave no false gap", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0,
			gapTreeNode("a", 0, 600, 1),
			gapTreeNode("b", 400, 1000, 1),
		)
		InsertGaps(root, 10)
		if got, want := childNames(root), []string{"a", "b"}; !equalStrings(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
	})

	t.Run("gap span IDs are unique and prefixed", func(t *testing.T) {
		root := gapTreeNode("root", 0, 1000, 0,
			gapTreeNode("a", 100, 300, 1),
			gapTreeNode("b", 600, 800, 1),
		)
		InsertGaps(root, 10)
		seen := map[string]bool{}
		for _, c := range root.Children {
			if c.Span.Name != gapName {
				continue
			}
			id := c.Span.SpanID
			if !strings.HasPrefix(id, "gap:") {
				t.Errorf("gap span ID %q lacks gap: prefix", id)
			}
			if seen[id] {
				t.Errorf("duplicate gap span ID %q", id)
			}
			seen[id] = true
		}
		if len(seen) != 3 { // leading, middle, trailing
			t.Errorf("expected 3 distinct gap IDs, got %d", len(seen))
		}
	})
}

func TestGapMinNs(t *testing.T) {
	tests := []struct {
		name    string
		rootDur int64
		width   int
		want    int64
	}{
		{"one char of a 4000ns root at width 40", 4000, 40, 100},
		{"rounds down to at least 1ns", 20, 40, 1},
		{"non-positive duration disables gaps", 0, 40, 0},
		{"non-positive width disables gaps", 1000, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GapMinNs(tt.rootDur, tt.width); got != tt.want {
				t.Errorf("GapMinNs(%d, %d) = %d, want %d", tt.rootDur, tt.width, got, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
