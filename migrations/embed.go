// Package migrations embeds Grotto's SQL schema so the store can apply it at
// open time without depending on files being present on disk.
package migrations

import _ "embed"

// Schema is the initial database schema (traces, spans, span_attributes, and
// their indexes). It is idempotent (CREATE ... IF NOT EXISTS) and safe to run on
// every Open.
//
//go:embed 001_init.sql
var Schema string
