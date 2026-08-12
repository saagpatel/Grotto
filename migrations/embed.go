// Package migrations embeds Grotto's SQL schema so the store can apply it at
// open time without depending on files being present on disk.
package migrations

import _ "embed"

// Schema is the ordered, additive database schema. Every statement is
// idempotent, so existing local databases gain later tables safely on Open.
//
//go:embed 001_init.sql
var initialSchema string

//go:embed 002_span_links.sql
var spanLinksSchema string

var Schema = initialSchema + "\n" + spanLinksSchema
