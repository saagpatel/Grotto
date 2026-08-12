// Command seed loads the deterministic P09 OTel-shaped trace fixture into the
// existing Grotto SQLite store for the five-minute local demo.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/saagpatel/grotto/internal/model"
	"github.com/saagpatel/grotto/internal/store"
)

const fixturePath = "internal/ledger/testdata/agent_trace.json"

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "p09 seed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	var trace model.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return fmt.Errorf("resolve store: %w", err)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.InsertTrace(ctx, trace); err != nil {
		return fmt.Errorf("insert trace: %w", err)
	}
	fmt.Println(trace.TraceID)
	return nil
}
