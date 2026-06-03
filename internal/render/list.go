package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/saagpatel/grotto/internal/store"
)

// WriteTraceList renders trace summaries as a newest-first table to w. The trace
// ID leads each row because it is the key passed to `grotto show` / `grotto diff`.
// now is the reference time for the age column (injected for testability). An
// empty slice renders a single placeholder line.
func WriteTraceList(w io.Writer, rows []store.TraceSummary, now time.Time) error {
	if len(rows) == 0 {
		_, err := io.WriteString(w, "no traces yet — capture one with `grotto run -- <cmd>` or `grotto serve`\n")
		return err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-34s  %6s  %10s  %-5s  %-12s  %s\n",
		"TRACE", "SPANS", "DURATION", "SRC", "AGE", "LABEL")
	for _, r := range rows {
		fmt.Fprintf(&sb, "%-34s  %6d  %10s  %-5s  %-12s  %s\n",
			r.TraceID, r.SpanCount, FormatDuration(r.DurationNs),
			r.Source, HumanAge(r.CreatedAt, now), r.RunLabel)
	}

	_, err := io.WriteString(w, sb.String())
	return err
}
