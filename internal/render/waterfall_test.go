package render

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	require.NoError(t, WriteWaterfall(&buf, renderFixtureTrace(), DefaultMaxRows))

	if os.Getenv("GROTTO_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(goldenPath(), buf.Bytes(), 0o644))
	}

	want, err := os.ReadFile(goldenPath())
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())
}

func TestWaterfall_EmptyTrace(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteWaterfall(&buf, model.Trace{}, DefaultMaxRows))
	assert.Equal(t, "(empty trace)\n", buf.String())
}

func TestWriteJSON_ContainsSpansAndParents(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteJSON(&buf, renderFixtureTrace()))

	out := buf.String()
	assert.Contains(t, out, `"span_count": 6`)
	assert.Contains(t, out, `"parent_span_id": "build"`)
	assert.Contains(t, out, `"name": "compile"`)
	assert.Contains(t, out, `"kind": "internal"`)
	assert.Contains(t, out, `"status": "ok"`)
	assert.NotContains(t, out, `"kind": 1`)
	assert.NotContains(t, out, `"status": 1`)
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
		assert.Equalf(t, want, FormatDuration(ns), "FormatDuration(%d)", ns)
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ago := func(sec int64) int64 { return now.Add(-time.Duration(sec) * time.Second).UnixNano() }

	assert.Equal(t, "30s ago", HumanAge(ago(30), now))
	assert.Equal(t, "5m ago", HumanAge(ago(300), now))
	assert.Equal(t, "2h ago", HumanAge(ago(7200), now))
	assert.Equal(t, "3d ago", HumanAge(ago(259200), now))
	assert.Equal(t, "0s ago", HumanAge(now.Add(time.Hour).UnixNano(), now), "future timestamps clamp to 0")
}

func TestFormatTimestamp(t *testing.T) {
	// Rendered in UTC regardless of the machine's zone, matching OTel's canonical
	// time base, so the assertion holds anywhere.
	tm := time.Date(2026, 6, 3, 9, 8, 5, 123_000_000, time.UTC)
	assert.Equal(t, "2026-06-03 09:08:05.123 UTC", FormatTimestamp(tm.UnixNano()))
}
