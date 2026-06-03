// Package store persists and retrieves Grotto traces in a local SQLite database
// using the pure-Go modernc.org/sqlite driver (no cgo, single static binary).
package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/saagpatel/grotto/migrations"
)

// Store is a handle to Grotto's SQLite-backed trace history.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and applies the
// embedded schema. Foreign keys and a busy timeout are enabled per connection so
// cascading deletes work and concurrent writers back off rather than erroring.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// Serialize access to the single local database file. This guarantees the
	// per-connection foreign_keys pragma always holds (no pooled connection can
	// silently have it off) and removes "database is locked" races between
	// writers — the OTLP receiver in Phase 2 depends on a single writer.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, migrations.Schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}
