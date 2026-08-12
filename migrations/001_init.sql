CREATE TABLE IF NOT EXISTS traces (
    trace_id    TEXT PRIMARY KEY,
    run_label   TEXT NOT NULL,
    source      TEXT NOT NULL,            -- 'mark' | 'otlp'
    root_name   TEXT NOT NULL,
    started_ns  INTEGER NOT NULL,
    ended_ns    INTEGER NOT NULL,
    duration_ns INTEGER NOT NULL,
    span_count  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS spans (
    span_id        TEXT PRIMARY KEY,
    trace_id       TEXT NOT NULL REFERENCES traces(trace_id) ON DELETE CASCADE,
    parent_span_id TEXT,                  -- NULL for root
    name           TEXT NOT NULL,
    kind           INTEGER NOT NULL,      -- OTel SpanKind 0..5
    status_code    INTEGER NOT NULL,      -- 0 unset, 1 ok, 2 error
    started_ns     INTEGER NOT NULL,
    ended_ns       INTEGER NOT NULL,
    duration_ns    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS span_attributes (
    span_id    TEXT NOT NULL REFERENCES spans(span_id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_type TEXT NOT NULL,             -- 'str'|'int'|'float'|'bool'|'bytes'|'json'
    value      TEXT NOT NULL,
    PRIMARY KEY (span_id, key)
);

CREATE INDEX IF NOT EXISTS idx_spans_trace    ON spans(trace_id);
CREATE INDEX IF NOT EXISTS idx_spans_parent   ON spans(parent_span_id);
CREATE INDEX IF NOT EXISTS idx_traces_started ON traces(started_ns DESC);
CREATE INDEX IF NOT EXISTS idx_attr_span      ON span_attributes(span_id);
