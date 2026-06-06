package cli

import (
	"testing"

	"github.com/saagpatel/grotto/internal/adapter"
	"github.com/saagpatel/grotto/internal/model"
)

// TestWithoutSections drops only the spans carrying the cargo.section marker,
// leaving crates and non-cargo spans untouched.
func TestWithoutSections(t *testing.T) {
	spans := []model.Span{
		{SpanID: "root", Name: "cargo"},
		{SpanID: "crate", Name: "serde v1", Attributes: []model.Attribute{{Key: "cargo.unit", Value: "0"}}},
		{SpanID: "fe", Name: "frontend", Attributes: []model.Attribute{{Key: adapter.AttrSection, Value: "frontend"}}},
		{SpanID: "cg", Name: "codegen", Attributes: []model.Attribute{{Key: adapter.AttrSection, Value: "codegen"}}},
	}

	got := withoutSections(spans)
	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2 (root + crate; sections dropped)", len(got))
	}
	for _, s := range got {
		if hasAttr(s, adapter.AttrSection) {
			t.Errorf("span %q is a section and should have been dropped", s.Name)
		}
	}
}
