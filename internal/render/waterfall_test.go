package render

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/saagpatel/grotto/internal/model"
)

// goldenPath is the committed expected waterfall for renderFixtureTrace. It is
// rendered by the same WriteWaterfall code path `grotto show` uses; regenerate
// with GROTTO_UPDATE_GOLDEN=1 go test ./internal/render/... after intentional
// format changes.
func goldenPath() string {
	return filepath.Join("..", "..", "tests", "fixtures", "expected-waterfall.txt")
}

func TestWaterfall_Golden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteWaterfall(&buf, renderFixtureTrace()))

	if os.Getenv("GROTTO_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(goldenPath(), buf.Bytes(), 0o644))
	}

	want, err := os.ReadFile(goldenPath())
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())
}

func TestWaterfall_EmptyTrace(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteWaterfall(&buf, model.Trace{}))
	assert.Equal(t, "(empty trace)\n", buf.String())
}

func TestWriteJSON_ContainsSpansAndParents(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteJSON(&buf, renderFixtureTrace()))

	out := buf.String()
	assert.Contains(t, out, `"span_count": 6`)
	assert.Contains(t, out, `"parent_span_id": "build"`)
	assert.Contains(t, out, `"name": "compile"`)
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{
		600 * ms:    "600ms",
		1_500 * ms:  "1.50s",
		90 * ms:     "90ms",
		500 * 1_000: "500µs",
		42:          "42ns",
	}
	for ns, want := range cases {
		assert.Equalf(t, want, formatDuration(ns), "formatDuration(%d)", ns)
	}
}
