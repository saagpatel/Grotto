package redaction

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEvaluatorHasNoNetworkImports is a structural guard for P08's local-only
// contract. It rejects direct network-capable imports in production evaluator
// files; tests and callers may still use ordinary filesystem and SQLite APIs.
func TestEvaluatorHasNoNetworkImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	forbidden := []string{"net", "net/", "google.golang.org/grpc", "golang.org/x/net"}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			for _, prefix := range forbidden {
				require.Falsef(t, importPath == prefix || strings.HasPrefix(importPath, prefix),
					"production evaluator file %s imports network-capable package %s", path, importPath)
			}
		}
	}
}
