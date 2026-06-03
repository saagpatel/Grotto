package store

import (
	"regexp"

	"github.com/saagpatel/grotto/internal/model"
)

// redactionMask replaces any matched credential before a trace is persisted.
const redactionMask = "‹redacted›"

// secretPatterns are the credential shapes scrubbed from span text on ingest.
// Each is anchored on a distinctive prefix and a length floor so ordinary build
// output is not mangled — the goal is "no plaintext secret reaches disk", not a
// general secret scanner.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),             // AWS access key ID
	regexp.MustCompile(`ghp_[0-9A-Za-z]{36}`),          // GitHub personal access token
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),          // OpenAI-style secret key
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), // Slack token
}

// redactString masks every credential match in s.
func redactString(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redactionMask)
	}
	return s
}

// Redact returns a copy of t with credential-shaped substrings masked in the
// run/root labels, span names, and attribute values. Timing fields and counts
// are untouched. Both capture paths converge on InsertTrace, so applying Redact
// there scrubs every ingested trace at a single chokepoint before it is written.
//
// Attribute keys are intentionally out of scope: they are static schema
// identifiers (http.method, db.system, …), an unlikely secret carrier, and
// masking them could collapse two keys on one span to the same value and trip
// the span_attributes (span_id, key) primary key — rejecting the whole trace.
// Over-masking a non-secret value (the conservative failure) is preferred to
// dropping a trace.
func Redact(t model.Trace) model.Trace {
	t.RunLabel = redactString(t.RunLabel)
	t.RootName = redactString(t.RootName)

	spans := make([]model.Span, len(t.Spans))
	for i, sp := range t.Spans {
		sp.Name = redactString(sp.Name)
		if len(sp.Attributes) > 0 {
			attrs := make([]model.Attribute, len(sp.Attributes))
			for j, a := range sp.Attributes {
				a.Value = redactString(a.Value)
				attrs[j] = a
			}
			sp.Attributes = attrs
		}
		spans[i] = sp
	}
	t.Spans = spans
	return t
}
