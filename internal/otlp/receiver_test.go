package otlp

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// grpcExport runs an in-process gRPC server over bufconn, exports req, drains
// the sink, and returns the sink's error output.
func grpcExport(t *testing.T, sink *Sink, req *coltracepb.ExportTraceServiceRequest) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := newGRPCServer(sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = coltracepb.NewTraceServiceClient(conn).Export(context.Background(), req)
	require.NoError(t, err)
}

func TestWarnIfPublic(t *testing.T) {
	mustAddr := func(s string) net.Addr {
		a, err := net.ResolveTCPAddr("tcp", s)
		require.NoError(t, err)
		return a
	}

	var loopback bytes.Buffer
	warnIfPublic(&loopback, "gRPC", mustAddr("127.0.0.1:4317"))
	assert.Empty(t, loopback.String(), "loopback bind does not warn")

	var public bytes.Buffer
	warnIfPublic(&public, "HTTP", mustAddr("0.0.0.0:4318"))
	assert.Contains(t, public.String(), "not loopback", "non-loopback bind warns")
}

func TestGRPCExport_StoresTrace(t *testing.T) {
	st := newStore(t)
	var errs bytes.Buffer
	sink := NewSink(st, 0, io.Discard, &errs)

	grpcExport(t, sink, threeSpanRequest())
	sink.Close()

	assert.Empty(t, errs.String(), "no storage errors")
	tr, err := st.GetTrace(context.Background(), fxTraceIDHex)
	require.NoError(t, err)
	assert.Equal(t, "otlp", tr.Source)
	assert.Equal(t, 3, tr.SpanCount)
}

func TestHTTPExport_StoresTrace(t *testing.T) {
	st := newStore(t)
	var errs bytes.Buffer
	sink := NewSink(st, 0, io.Discard, &errs)
	srv := httptest.NewServer(newHTTPHandler(sink))
	t.Cleanup(srv.Close)

	body, err := proto.Marshal(threeSpanRequest())
	require.NoError(t, err)
	resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	sink.Close()
	assert.Empty(t, errs.String())
	tr, err := st.GetTrace(context.Background(), fxTraceIDHex)
	require.NoError(t, err)
	assert.Equal(t, 3, tr.SpanCount)
}

// TestBothTransports_IdenticalTrees verifies the same export over gRPC and HTTP
// yields byte-identical stored trees (validation criterion 5).
func TestBothTransports_IdenticalTrees(t *testing.T) {
	ctx := context.Background()

	stG := newStore(t)
	sinkG := NewSink(stG, 0, io.Discard, io.Discard)
	grpcExport(t, sinkG, threeSpanRequest())
	sinkG.Close()
	viaGRPC, err := stG.GetTrace(ctx, fxTraceIDHex)
	require.NoError(t, err)

	stH := newStore(t)
	sinkH := NewSink(stH, 0, io.Discard, io.Discard)
	srv := httptest.NewServer(newHTTPHandler(sinkH))
	t.Cleanup(srv.Close)
	body, err := proto.Marshal(threeSpanRequest())
	require.NoError(t, err)
	resp, err := http.Post(srv.URL+"/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	sinkH.Close()
	viaHTTP, err := stH.GetTrace(ctx, fxTraceIDHex)
	require.NoError(t, err)

	assert.Equal(t, viaGRPC, viaHTTP)
}

// TestExport_200Spans_NoLockErrors stores a 200-span trace across 10 runs with no
// "database is locked" errors (validation criterion 4).
func TestExport_200Spans_NoLockErrors(t *testing.T) {
	for run := 0; run < 10; run++ {
		st := newStore(t)
		var errs bytes.Buffer
		sink := NewSink(st, 0, io.Discard, &errs)

		sink.Submit(MapExportRequest(bigRequest(200)))
		sink.Close()

		require.Emptyf(t, errs.String(), "run %d storage errors", run)
		tr, err := st.GetTrace(context.Background(), bigTraceIDHex())
		require.NoErrorf(t, err, "run %d", run)
		assert.Equalf(t, 200, tr.SpanCount, "run %d span count", run)
	}
}

// TestThreeSpanJSONFixture round-trips the committed three-span-trace.json over
// the OTLP/HTTP JSON path. Regenerate with GROTTO_UPDATE_GOLDEN=1.
func TestThreeSpanJSONFixture(t *testing.T) {
	fixture := filepath.Join("..", "..", "tests", "fixtures", "three-span-trace.json")
	if os.Getenv("GROTTO_UPDATE_GOLDEN") != "" {
		data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(threeSpanRequest())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(fixture, data, 0o644))
	}

	data, err := os.ReadFile(fixture)
	require.NoError(t, err)

	st := newStore(t)
	sink := NewSink(st, 0, io.Discard, io.Discard)
	srv := httptest.NewServer(newHTTPHandler(sink))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/traces", "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sink.Close()
	tr, err := st.GetTrace(context.Background(), fxTraceIDHex)
	require.NoError(t, err)
	assert.Equal(t, 3, tr.SpanCount)
	assert.Equal(t, "otlp", tr.Source)
}
