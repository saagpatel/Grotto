package otlp

import (
	"io"
	"net/http"
	"strings"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxBodyBytes caps an OTLP/HTTP request body (4 MiB) to bound memory per export.
const maxBodyBytes = 4 << 20

// newHTTPHandler returns the OTLP/HTTP handler serving POST /v1/traces. It
// accepts protobuf (application/x-protobuf, the default) and JSON
// (application/json) bodies and replies in the same encoding.
func newHTTPHandler(sink *Sink) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		isJSON := strings.Contains(r.Header.Get("Content-Type"), "json")
		var req coltracepb.ExportTraceServiceRequest
		if isJSON {
			err = protojson.Unmarshal(body, &req)
		} else {
			err = proto.Unmarshal(body, &req)
		}
		if err != nil {
			http.Error(w, "decode export request: "+err.Error(), http.StatusBadRequest)
			return
		}

		sink.Submit(MapExportRequest(&req))

		writeResponse(w, isJSON)
	})
	return mux
}

// writeResponse encodes an empty (full-success) export response in the request's
// encoding.
func writeResponse(w http.ResponseWriter, isJSON bool) {
	resp := &coltracepb.ExportTraceServiceResponse{}
	var (
		body []byte
		err  error
		ct   string
	)
	if isJSON {
		body, err = protojson.Marshal(resp)
		ct = "application/json"
	} else {
		body, err = proto.Marshal(resp)
		ct = "application/x-protobuf"
	}
	if err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	// The trace is already queued; a failed response write means the client
	// disconnected, which is not actionable here.
	_, _ = w.Write(body)
}
