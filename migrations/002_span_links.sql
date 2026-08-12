CREATE TABLE IF NOT EXISTS span_diagnostics (
    span_id                  TEXT PRIMARY KEY REFERENCES spans(span_id) ON DELETE CASCADE,
    dropped_attributes_count INTEGER NOT NULL DEFAULT 0,
    dropped_links_count      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS span_links (
    span_id                  TEXT NOT NULL REFERENCES spans(span_id) ON DELETE CASCADE,
    link_index               INTEGER NOT NULL,
    trace_id                 TEXT NOT NULL,
    linked_span_id           TEXT NOT NULL,
    trace_state              TEXT NOT NULL DEFAULT '',
    dropped_attributes_count INTEGER NOT NULL DEFAULT 0,
    flags                    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (span_id, link_index)
);

CREATE TABLE IF NOT EXISTS span_link_attributes (
    span_id    TEXT NOT NULL,
    link_index INTEGER NOT NULL,
    key        TEXT NOT NULL,
    value_type TEXT NOT NULL,
    value      TEXT NOT NULL,
    PRIMARY KEY (span_id, link_index, key),
    FOREIGN KEY (span_id, link_index) REFERENCES span_links(span_id, link_index) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_span_links_target ON span_links(trace_id, linked_span_id);
