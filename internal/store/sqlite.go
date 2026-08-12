// Package store persists and retrieves Grotto traces in a local SQLite database
// using the pure-Go modernc.org/sqlite driver (no cgo, single static binary).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/saagpatel/grotto/migrations"
)

// DefaultDBPath returns the path to the local trace database, honoring the
// GROTTO_DB override and otherwise defaulting to ~/.grotto/grotto.db.
func DefaultDBPath() (string, error) {
	if p := os.Getenv("GROTTO_DB"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".grotto", "grotto.db"), nil
}

// Store is a handle to Grotto's SQLite-backed trace history.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and applies the
// embedded schema. Foreign keys and a busy timeout are enabled per connection so
// cascading deletes work and concurrent writers back off rather than erroring.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %q: %w", dir, err)
		}
	}
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

// OpenReadOnly opens an existing SQLite database without creating directories
// or applying migrations. It keeps SQLite locking and change detection enabled
// so a preview can safely coexist with a writer and observe later commits.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite read-only path %q: %w", path, err)
	}
	if _, err := os.Stat(absolutePath); err != nil {
		return nil, fmt.Errorf("stat sqlite %q: %w", path, err)
	}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Set("_pragma", "busy_timeout(5000)")
	dsnURL := &url.URL{Scheme: "file", Path: absolutePath, RawQuery: query.Encode()}
	dsn := dsnURL.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite read-only %q: %w", path, err)
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
