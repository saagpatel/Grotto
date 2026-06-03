package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// emitTimeout bounds dial and write on the mark socket so `grotto mark` can never
// hang a build; on timeout it falls back to the spool file.
const emitTimeout = time.Second

// Emit records a mark named name, timestamped at call time, for the active run.
// It prefers the Unix domain socket at GROTTO_SOCK and falls back to appending a
// JSONL line to GROTTO_SPOOL if the socket is unreachable or does not acknowledge
// the mark. It returns an error when invoked outside a `grotto run`.
func Emit(name string) error {
	sock := os.Getenv(EnvSock)
	spool := os.Getenv(EnvSpool)
	if sock == "" && spool == "" {
		return errors.New("not inside a grotto run (GROTTO_SOCK unset)")
	}

	m := Mark{Name: name, TSNs: time.Now().UnixNano()}
	if sock != "" {
		if err := emitSocket(sock, m); err == nil {
			return nil // acknowledged by the run
		}
		// Socket unreachable or unacknowledged — fall through to the spool. A
		// run dedups marks by (name, timestamp), so a mark that ends up in both
		// places is recorded only once.
	}
	if spool != "" {
		return emitSpool(spool, m)
	}
	return fmt.Errorf("run socket %q unreachable and no spool configured", sock)
}

func emitSocket(sock string, m Mark) error {
	conn, err := net.DialTimeout("unix", sock, emitTimeout)
	if err != nil {
		return fmt.Errorf("dial %q: %w", sock, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(emitTimeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		return fmt.Errorf("write mark: %w", err)
	}
	// Block until the collector acknowledges by writing one byte back, which it
	// does only after recording the mark. This makes delivery synchronous: a
	// missing ack (timeout, reset on a closed listener) returns an error so the
	// caller falls back to the spool rather than silently losing the mark.
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		return fmt.Errorf("await ack: %w", err)
	}
	return nil
}

func emitSpool(spool string, m Mark) error {
	f, err := os.OpenFile(spool, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open spool %q: %w", spool, err)
	}
	defer func() { _ = f.Close() }()

	line, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal mark: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append spool %q: %w", spool, err)
	}
	return nil
}
