package otlp

import (
	"context"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// grpcReceiver implements the OTLP TraceService gRPC server, mapping each export
// onto the shared sink.
type grpcReceiver struct {
	coltracepb.UnimplementedTraceServiceServer
	sink *Sink
}

// Export maps the request's spans to traces and queues them for storage. It
// always reports full success; persistence happens asynchronously in the sink.
func (g *grpcReceiver) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	g.sink.Submit(MapExportRequest(req))
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// newGRPCServer builds a gRPC server with the TraceService registered.
func newGRPCServer(sink *Sink) *grpc.Server {
	srv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(srv, &grpcReceiver{sink: sink})
	return srv
}
