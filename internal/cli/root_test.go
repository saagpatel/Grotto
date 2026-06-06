package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestRootVersionFlag verifies the root command exposes --version and prints the
// build version through the custom "grotto <version>" template.
func TestRootVersionFlag(t *testing.T) {
	root := NewRootCmd()
	if root.Version == "" {
		t.Fatal("root command Version is empty; --version would be unavailable")
	}

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}

	got := out.String()
	want := "grotto " + root.Version + "\n"
	if got != want {
		t.Errorf("--version output = %q, want %q", got, want)
	}
	if strings.Contains(got, "version version") {
		t.Errorf("--version output uses cobra's default template, not the custom one: %q", got)
	}
}
