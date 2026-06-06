package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestLookup verifies the registry: known adapters are found, unknown names and
// the empty string (meaning "no adapter") both return ok=false.
func TestLookup(t *testing.T) {
	t.Run("cargo returns ok", func(t *testing.T) {
		a, ok := Lookup("cargo")
		if !ok {
			t.Fatal("Lookup(\"cargo\") returned ok=false, want true")
		}
		if a.Name() != "cargo" {
			t.Errorf("Name() = %q, want %q", a.Name(), "cargo")
		}
	})

	t.Run("unknown name returns not ok", func(t *testing.T) {
		if _, ok := Lookup("pytest"); ok {
			t.Error("Lookup(\"pytest\") returned ok=true, want false")
		}
	})

	t.Run("empty string returns not ok", func(t *testing.T) {
		if _, ok := Lookup(""); ok {
			t.Error("Lookup(\"\") returned ok=true, want false")
		}
	})
}

// TestCargoAdapter_PrepareArgv verifies that --timings is injected exactly once
// and that existing flags are never duplicated.
func TestCargoAdapter_PrepareArgv(t *testing.T) {
	ad := cargoAdapter{}

	tests := []struct {
		name     string
		input    []string
		wantLast string // expected last element of output; "" means verify length
		wantLen  int    // expected slice length
	}{
		{
			name:     "injects --timings when absent",
			input:    []string{"cargo", "build"},
			wantLast: "--timings",
			wantLen:  3,
		},
		{
			name:    "idempotent when --timings already present",
			input:   []string{"cargo", "build", "--timings"},
			wantLen: 3,
		},
		{
			name:    "idempotent when --timings=html already present",
			input:   []string{"cargo", "build", "--timings=html"},
			wantLen: 3,
		},
		{
			name:     "injects into multi-arg list",
			input:    []string{"cargo", "test", "--release", "--", "--nocapture"},
			wantLast: "--timings",
			wantLen:  6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := ad.PrepareArgv(tc.input)
			if len(out) != tc.wantLen {
				t.Errorf("len(PrepareArgv) = %d, want %d; got %v", len(out), tc.wantLen, out)
			}
			if tc.wantLast != "" && out[len(out)-1] != tc.wantLast {
				t.Errorf("PrepareArgv last element = %q, want %q", out[len(out)-1], tc.wantLast)
			}
		})
	}
}

// TestCargoAdapter_ParseSpans_NoMarker verifies the benign degradation path:
// when stderr contains no "Timing report saved to" line, ParseSpans must return
// (nil, nil) so the run degrades to the root span only, not an error.
func TestCargoAdapter_ParseSpans_NoMarker(t *testing.T) {
	ad := cargoAdapter{}
	bc := BuildContext{
		RootID:    "rootspan",
		TraceID:   "traceid",
		StartNs:   1_000_000_000,
		Stderr:    []byte("error[E0308]: mismatched types\n   --> src/main.rs:5:10\n"),
		NewSpanID: func() string { return "x" },
	}

	spans, err := ad.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Errorf("ParseSpans without marker returned err=%v, want nil", err)
	}
	if spans != nil {
		t.Errorf("ParseSpans without marker returned %d spans, want nil", len(spans))
	}
}

// TestCargoAdapter_ParseSpans_RealFixture is the end-to-end test: it writes the
// real fixture path into a fake stderr buffer and asserts that ParseSpans returns
// exactly 4 spans (the 4 crates in the fixture), all parented to bc.RootID and
// carrying bc.TraceID.
func TestCargoAdapter_ParseSpans_RealFixture(t *testing.T) {
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "cargo-timing-fixture.html"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if _, err := os.Stat(fixturePath); err != nil {
		t.Skipf("fixture not present: %v", err)
	}

	// Construct a stderr buffer that mirrors what cargo actually prints.
	fakeStderr := fmt.Sprintf("   Compiling itoa v1.0.18\nTiming report saved to %s\n", fixturePath)

	counter := 0
	newID := func() string {
		counter++
		return fmt.Sprintf("span-%04d", counter)
	}

	ad := cargoAdapter{}
	bc := BuildContext{
		RootID:    "root0000",
		TraceID:   "trace0000",
		StartNs:   5_000_000_000,
		EndNs:     10_000_000_000,
		Stderr:    []byte(fakeStderr),
		NewSpanID: newID,
	}

	spans, err := ad.ParseSpans(context.Background(), bc)
	if err != nil {
		t.Fatalf("ParseSpans: %v", err)
	}

	// Separate crate spans (one per compilation unit, carrying cargo.unit) from
	// their frontend/codegen sub-phase children (carrying cargo.section).
	var crates, sub int
	for i, s := range spans {
		isSection := false
		for _, a := range s.Attributes {
			if a.Key == AttrSection {
				isSection = true
			}
		}
		if s.TraceID != bc.TraceID {
			t.Errorf("spans[%d].TraceID = %q, want %q", i, s.TraceID, bc.TraceID)
		}
		if s.StartedNs < bc.StartNs {
			t.Errorf("spans[%d].StartedNs %d < StartNs %d (anchor violation)", i, s.StartedNs, bc.StartNs)
		}
		if isSection {
			sub++
			if s.ParentSpanID == bc.RootID {
				t.Errorf("section spans[%d] must parent to a crate, not the root", i)
			}
		} else {
			crates++
			if s.ParentSpanID != bc.RootID {
				t.Errorf("crate spans[%d].ParentSpanID = %q, want root %q", i, s.ParentSpanID, bc.RootID)
			}
		}
	}

	// The fixture contains exactly 4 compilation units, each with sub-phases.
	if crates != 4 {
		t.Errorf("got %d crate spans, want 4", crates)
	}
	if sub == 0 {
		t.Error("expected frontend/codegen sub-phase spans from the fixture, got none")
	}
}
