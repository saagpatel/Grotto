package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/saagpatel/grotto/internal/model"
)

const insertTraceSQL = `
INSERT INTO traces (trace_id, run_label, source, root_name, started_ns, ended_ns, duration_ns, span_count, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertSpanSQL = `
INSERT INTO spans (span_id, trace_id, parent_span_id, name, kind, status_code, started_ns, ended_ns, duration_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertAttrSQL = `
INSERT INTO span_attributes (span_id, key, value_type, value)
VALUES (?, ?, ?, ?)`

const insertSpanDiagnosticsSQL = `
INSERT INTO span_diagnostics (span_id, dropped_attributes_count, dropped_links_count)
VALUES (?, ?, ?)`

const insertSpanLinkSQL = `
INSERT INTO span_links (span_id, link_index, trace_id, linked_span_id, trace_state, dropped_attributes_count, flags)
VALUES (?, ?, ?, ?, ?, ?, ?)`

const insertSpanLinkAttrSQL = `
INSERT INTO span_link_attributes (span_id, link_index, key, value_type, value)
VALUES (?, ?, ?, ?, ?)`

const selectTraceSQL = `
SELECT trace_id, run_label, source, root_name, started_ns, ended_ns, duration_ns, span_count
FROM traces WHERE trace_id = ?`

const selectSpansSQL = `
SELECT s.span_id, s.parent_span_id, s.name, s.kind, s.status_code,
       s.started_ns, s.ended_ns, s.duration_ns,
       COALESCE(d.dropped_attributes_count, 0), COALESCE(d.dropped_links_count, 0)
FROM spans s
LEFT JOIN span_diagnostics d ON d.span_id = s.span_id
WHERE s.trace_id = ? ORDER BY s.started_ns, s.span_id`

const selectAttrsSQL = `
SELECT a.span_id, a.key, a.value_type, a.value
FROM span_attributes a
JOIN spans s ON s.span_id = a.span_id
WHERE s.trace_id = ?
ORDER BY a.span_id, a.key`

const selectLinksSQL = `
SELECT l.span_id, l.link_index, l.trace_id, l.linked_span_id, l.trace_state,
       l.dropped_attributes_count, l.flags
FROM span_links l
JOIN spans s ON s.span_id = l.span_id
WHERE s.trace_id = ?
ORDER BY l.span_id, l.link_index`

const selectLinkAttrsSQL = `
SELECT a.span_id, a.link_index, a.key, a.value_type, a.value
FROM span_link_attributes a
JOIN spans s ON s.span_id = a.span_id
WHERE s.trace_id = ?
ORDER BY a.span_id, a.link_index, a.key`

const selectRecentTracesSQL = `
SELECT trace_id, run_label, source, root_name, started_ns, duration_ns, span_count, created_at
FROM traces ORDER BY started_ns DESC LIMIT ?`

// InsertTrace persists a trace with all of its spans and attributes in a single
// transaction. On any failure the transaction is rolled back and the error is
// returned (wrapped). created_at is stamped at write time; it is store metadata,
// not part of the span model.
func (s *Store) InsertTrace(ctx context.Context, t model.Trace) (err error) {
	// Scrub credential-shaped strings before anything touches disk. Both capture
	// paths (marks + OTLP) funnel through here, so this is the one place redaction
	// has to live.
	t, err = Redact(t)
	if err != nil {
		return fmt.Errorf("redact trace %q: %w", t.TraceID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
	}()

	if _, err = tx.ExecContext(ctx, insertTraceSQL,
		t.TraceID, t.RunLabel, t.Source, t.RootName,
		t.StartedNs, t.EndedNs, t.DurationNs, t.SpanCount, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("insert trace %q: %w", t.TraceID, err)
	}

	for _, sp := range t.Spans {
		// Store the root's parent as SQL NULL rather than an empty string.
		var parent any
		if sp.ParentSpanID != "" {
			parent = sp.ParentSpanID
		}
		if _, err = tx.ExecContext(ctx, insertSpanSQL,
			sp.SpanID, t.TraceID, parent, sp.Name,
			int32(sp.Kind), int32(sp.Status),
			sp.StartedNs, sp.EndedNs, sp.DurationNs); err != nil {
			return fmt.Errorf("insert span %q: %w", sp.SpanID, err)
		}
		for _, a := range sp.Attributes {
			if _, err = tx.ExecContext(ctx, insertAttrSQL,
				sp.SpanID, a.Key, a.ValueType, a.Value); err != nil {
				return fmt.Errorf("insert attr %q/%q: %w", sp.SpanID, a.Key, err)
			}
		}
		if _, err = tx.ExecContext(ctx, insertSpanDiagnosticsSQL,
			sp.SpanID, sp.DroppedAttributesCount, sp.DroppedLinksCount); err != nil {
			return fmt.Errorf("insert span diagnostics %q: %w", sp.SpanID, err)
		}
		for linkIndex, link := range sp.Links {
			if _, err = tx.ExecContext(ctx, insertSpanLinkSQL,
				sp.SpanID, linkIndex, link.TraceID, link.SpanID, link.TraceState,
				link.DroppedAttributesCount, link.Flags); err != nil {
				return fmt.Errorf("insert span link %q/%d: %w", sp.SpanID, linkIndex, err)
			}
			for _, a := range link.Attributes {
				if _, err = tx.ExecContext(ctx, insertSpanLinkAttrSQL,
					sp.SpanID, linkIndex, a.Key, a.ValueType, a.Value); err != nil {
					return fmt.Errorf("insert span link attr %q/%d/%q: %w", sp.SpanID, linkIndex, a.Key, err)
				}
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit trace %q: %w", t.TraceID, err)
	}
	return nil
}

// GetTrace loads a trace, its spans (ordered by start time), and each span's
// attributes (ordered by key). It returns sql.ErrNoRows (wrapped) when no trace
// has the given id.
func (s *Store) GetTrace(ctx context.Context, traceID string) (model.Trace, error) {
	var t model.Trace
	row := s.db.QueryRowContext(ctx, selectTraceSQL, traceID)
	if err := row.Scan(&t.TraceID, &t.RunLabel, &t.Source, &t.RootName,
		&t.StartedNs, &t.EndedNs, &t.DurationNs, &t.SpanCount); err != nil {
		return model.Trace{}, fmt.Errorf("get trace %q: %w", traceID, err)
	}

	spans, err := s.loadSpans(ctx, traceID)
	if err != nil {
		return model.Trace{}, err
	}
	t.Spans = spans
	return t, nil
}

// loadSpans reads all spans for a trace (ordered by start time) and attaches
// each span's attributes. Attributes are fetched for the whole trace in one
// query to avoid an N+1 round trip; spans with no attributes keep a nil slice.
func (s *Store) loadSpans(ctx context.Context, traceID string) ([]model.Span, error) {
	rows, err := s.db.QueryContext(ctx, selectSpansSQL, traceID)
	if err != nil {
		return nil, fmt.Errorf("query spans for %q: %w", traceID, err)
	}
	defer func() { _ = rows.Close() }()

	var spans []model.Span
	for rows.Next() {
		var (
			sp     model.Span
			parent sql.NullString
			kind   int32
			status int32
		)
		if err := rows.Scan(&sp.SpanID, &parent, &sp.Name, &kind, &status,
			&sp.StartedNs, &sp.EndedNs, &sp.DurationNs,
			&sp.DroppedAttributesCount, &sp.DroppedLinksCount); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		sp.TraceID = traceID
		sp.ParentSpanID = parent.String // "" when NULL (root)
		sp.Kind = model.SpanKind(kind)
		sp.Status = model.StatusCode(status)
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate spans for %q: %w", traceID, err)
	}

	attrsBySpan, err := s.loadAttrs(ctx, traceID)
	if err != nil {
		return nil, err
	}
	for i := range spans {
		spans[i].Attributes = attrsBySpan[spans[i].SpanID]
	}
	linksBySpan, err := s.loadLinks(ctx, traceID)
	if err != nil {
		return nil, err
	}
	for i := range spans {
		spans[i].Links = linksBySpan[spans[i].SpanID]
	}
	return spans, nil
}

type linkKey struct {
	spanID string
	index  int
}

func (s *Store) loadLinks(ctx context.Context, traceID string) (map[string][]model.SpanLink, error) {
	attrs, err := s.loadLinkAttrs(ctx, traceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, selectLinksSQL, traceID)
	if err != nil {
		return nil, fmt.Errorf("query links for %q: %w", traceID, err)
	}
	defer func() { _ = rows.Close() }()

	bySpan := make(map[string][]model.SpanLink)
	for rows.Next() {
		var (
			spanID string
			index  int
			link   model.SpanLink
		)
		if err := rows.Scan(&spanID, &index, &link.TraceID, &link.SpanID,
			&link.TraceState, &link.DroppedAttributesCount, &link.Flags); err != nil {
			return nil, fmt.Errorf("scan span link: %w", err)
		}
		link.Attributes = attrs[linkKey{spanID: spanID, index: index}]
		bySpan[spanID] = append(bySpan[spanID], link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate links for %q: %w", traceID, err)
	}
	return bySpan, nil
}

func (s *Store) loadLinkAttrs(ctx context.Context, traceID string) (map[linkKey][]model.Attribute, error) {
	rows, err := s.db.QueryContext(ctx, selectLinkAttrsSQL, traceID)
	if err != nil {
		return nil, fmt.Errorf("query link attrs for %q: %w", traceID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[linkKey][]model.Attribute)
	for rows.Next() {
		var (
			key linkKey
			a   model.Attribute
		)
		if err := rows.Scan(&key.spanID, &key.index, &a.Key, &a.ValueType, &a.Value); err != nil {
			return nil, fmt.Errorf("scan link attr: %w", err)
		}
		out[key] = append(out[key], a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate link attrs for %q: %w", traceID, err)
	}
	return out, nil
}

// loadAttrs reads every attribute for a trace in one query and groups them by
// span ID (each group ordered by key). Spans with no attributes are simply
// absent from the map, so callers receive a nil slice for them.
func (s *Store) loadAttrs(ctx context.Context, traceID string) (map[string][]model.Attribute, error) {
	rows, err := s.db.QueryContext(ctx, selectAttrsSQL, traceID)
	if err != nil {
		return nil, fmt.Errorf("query attrs for %q: %w", traceID, err)
	}
	defer func() { _ = rows.Close() }()

	bySpan := make(map[string][]model.Attribute)
	for rows.Next() {
		var (
			spanID string
			a      model.Attribute
		)
		if err := rows.Scan(&spanID, &a.Key, &a.ValueType, &a.Value); err != nil {
			return nil, fmt.Errorf("scan attr: %w", err)
		}
		bySpan[spanID] = append(bySpan[spanID], a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attrs for %q: %w", traceID, err)
	}
	return bySpan, nil
}

// TraceSummary is run-level metadata for a stored trace, without its spans —
// enough to populate a run list without the cost of loading every span. CreatedAt
// is the store write time (nanoseconds), used for age display in the UI.
type TraceSummary struct {
	TraceID    string
	RunLabel   string
	Source     string
	RootName   string
	StartedNs  int64
	DurationNs int64
	SpanCount  int
	CreatedAt  int64
}

// RecentTraces returns up to limit trace summaries, newest first by start time
// (the started_ns DESC index backs the ordering). A non-positive limit yields no
// rows. Used by the TUI run list and, later, `grotto list`.
func (s *Store) RecentTraces(ctx context.Context, limit int) ([]TraceSummary, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, selectRecentTracesSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent traces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TraceSummary
	for rows.Next() {
		var ts TraceSummary
		if err := rows.Scan(&ts.TraceID, &ts.RunLabel, &ts.Source, &ts.RootName,
			&ts.StartedNs, &ts.DurationNs, &ts.SpanCount, &ts.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan trace summary: %w", err)
		}
		out = append(out, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent traces: %w", err)
	}
	return out, nil
}
