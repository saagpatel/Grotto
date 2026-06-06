package otlp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/saagpatel/grotto/internal/store"
)

// Config configures the OTLP receiver.
type Config struct {
	GRPCAddr string    // loopback gRPC bind, e.g. 127.0.0.1:4317
	HTTPAddr string    // loopback HTTP bind, e.g. 127.0.0.1:4318
	BufSize  int       // sink channel capacity (0 -> DefaultBufferSize)
	Out      io.Writer // status messages (nil -> discarded)
	ErrOut   io.Writer // asynchronous storage errors (nil -> discarded)
}

const (
	shutdownGrace   = 5 * time.Second
	httpReadTimeout = 30 * time.Second
)

// Serve runs the OTLP gRPC and HTTP receivers until ctx is canceled (e.g. by
// SIGINT), then shuts both down gracefully and drains the sink. It returns the
// first server error, or nil on a clean ctx-triggered shutdown.
func Serve(ctx context.Context, st *store.Store, cfg Config) error {
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.ErrOut == nil {
		cfg.ErrOut = io.Discard
	}

	sink := NewSink(st, cfg.BufSize, cfg.Out, cfg.ErrOut)
	defer sink.Close() // runs last: after both servers have stopped

	grpcLn, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC %q: %w", cfg.GRPCAddr, err)
	}
	httpLn, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		_ = grpcLn.Close()
		return fmt.Errorf("listen HTTP %q: %w", cfg.HTTPAddr, err)
	}

	grpcSrv := newGRPCServer(sink)
	httpSrv := &http.Server{
		Handler:           newHTTPHandler(sink),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       httpReadTimeout,
	}

	errc := make(chan error, 2)
	go func() { errc <- grpcSrv.Serve(grpcLn) }()
	go func() { errc <- httpSrv.Serve(httpLn) }()

	_, _ = fmt.Fprintf(cfg.Out, "grotto serve: OTLP gRPC on %s, HTTP on %s (ctrl-c to stop)\n",
		grpcLn.Addr(), httpLn.Addr())
	warnIfPublic(cfg.ErrOut, "gRPC", grpcLn.Addr())
	warnIfPublic(cfg.ErrOut, "HTTP", httpLn.Addr())

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		// Force-stop gRPC if it has not drained by the deadline (e.g. a stuck
		// connection) so shutdown is bounded. Owner: this call; exits when
		// shutCtx is done (deadline or the deferred cancel after we return).
		go func() {
			<-shutCtx.Done()
			grpcSrv.Stop()
		}()
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	case err := <-errc:
		// A server returned before shutdown was requested — an unexpected exit.
		grpcSrv.Stop()
		_ = httpSrv.Close()
		if err != nil {
			return fmt.Errorf("otlp receiver: %w", err)
		}
		return nil
	}
}

// warnIfPublic notes on errOut when an OTLP listener is bound to a non-loopback
// address, since the receiver accepts unauthenticated spans.
func warnIfPublic(errOut io.Writer, label string, addr net.Addr) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return
	}
	if host == "localhost" {
		return
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	_, _ = fmt.Fprintf(errOut,
		"grotto serve: warning: %s listener %s is not loopback; the OTLP receiver is unauthenticated\n",
		label, addr)
}
