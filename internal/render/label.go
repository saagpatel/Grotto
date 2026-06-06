package render

import "strings"

// CleanLabel renders a run label as a single scannable line: it collapses every
// run of internal whitespace (spaces, tabs, and newlines) to one space, then
// truncates the result to at most max runes, appending an ellipsis when it
// shortens. A non-positive max disables truncation. This keeps multi-line run
// labels — e.g. `bash -c '<script>'` — from spilling newlines into table rows
// and headers. Truncation is rune-aware so multibyte characters never split.
func CleanLabel(s string, max int) string {
	clean := strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return clean
	}
	r := []rune(clean)
	if len(r) <= max {
		return clean
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
