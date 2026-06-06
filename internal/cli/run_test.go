package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunCmd_UnknownAdapter asserts that supplying an unknown --adapter value
// produces an immediate user-facing error rather than starting the run. This
// guards the lookup gate added in v1.4.
func TestRunCmd_UnknownAdapter(t *testing.T) {
	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetErr(&errBuf)
	root.SetArgs([]string{"run", "--adapter=bogus", "--", "true"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for unknown adapter, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q should mention the unknown adapter name %q", err.Error(), "bogus")
	}
}
