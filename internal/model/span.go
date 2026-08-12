// Package model defines the OpenTelemetry-shaped span types that flow through
// Grotto's capture, storage, and rendering layers. Both capture paths (grotto
// marks and the OTLP receiver) converge on these types so there is exactly one
// span model in the system — deliberately genuine OTel shapes, not a homegrown
// timestamp format.
package model

import (
	"encoding/json"
	"fmt"
	"sort"
)

// SpanKind mirrors the OpenTelemetry SpanKind enumeration (0..5).
type SpanKind int32

// OpenTelemetry span kinds.
const (
	KindUnspecified SpanKind = 0
	KindInternal    SpanKind = 1
	KindServer      SpanKind = 2
	KindClient      SpanKind = 3
	KindProducer    SpanKind = 4
	KindConsumer    SpanKind = 5
)

func (k SpanKind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindServer:
		return "server"
	case KindClient:
		return "client"
	case KindProducer:
		return "producer"
	case KindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

func (k SpanKind) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

// UnmarshalJSON accepts the readable labels emitted by MarshalJSON and numeric
// OTLP enum values for imported Grotto trace files.
func (k *SpanKind) UnmarshalJSON(data []byte) error {
	var label string
	if err := json.Unmarshal(data, &label); err == nil {
		switch label {
		case "unspecified":
			*k = KindUnspecified
		case "internal":
			*k = KindInternal
		case "server":
			*k = KindServer
		case "client":
			*k = KindClient
		case "producer":
			*k = KindProducer
		case "consumer":
			*k = KindConsumer
		default:
			return fmt.Errorf("unknown span kind %q", label)
		}
		return nil
	}
	var value int32
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode span kind: %w", err)
	}
	if value < int32(KindUnspecified) || value > int32(KindConsumer) {
		return fmt.Errorf("span kind %d is out of range", value)
	}
	*k = SpanKind(value)
	return nil
}

// StatusCode mirrors the OpenTelemetry status code enumeration.
type StatusCode int32

// OpenTelemetry status codes.
const (
	StatusUnset StatusCode = 0
	StatusOk    StatusCode = 1
	StatusError StatusCode = 2
)

func (s StatusCode) String() string {
	switch s {
	case StatusOk:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}

func (s StatusCode) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON accepts the readable labels emitted by MarshalJSON and numeric
// OTLP enum values for imported Grotto trace files.
func (s *StatusCode) UnmarshalJSON(data []byte) error {
	var label string
	if err := json.Unmarshal(data, &label); err == nil {
		switch label {
		case "unset":
			*s = StatusUnset
		case "ok":
			*s = StatusOk
		case "error":
			*s = StatusError
		default:
			return fmt.Errorf("unknown status code %q", label)
		}
		return nil
	}
	var value int32
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode status code: %w", err)
	}
	if value < int32(StatusUnset) || value > int32(StatusError) {
		return fmt.Errorf("status code %d is out of range", value)
	}
	*s = StatusCode(value)
	return nil
}

// Attribute is a single typed key/value pair on a span. Value is stored as a
// string; type-assert against ValueType
// ("str"|"int"|"float"|"bool"|"bytes"|"json") to recover the original type.
type Attribute struct {
	Key       string `json:"key"`
	ValueType string `json:"value_type"`
	Value     string `json:"value"`
}

// Span is a single OpenTelemetry span. ParentSpanID is empty for a root span.
type Span struct {
	SpanID       string      `json:"span_id"`
	TraceID      string      `json:"trace_id"`
	ParentSpanID string      `json:"parent_span_id"`
	Name         string      `json:"name"`
	Kind         SpanKind    `json:"kind"`
	Status       StatusCode  `json:"status"`
	StartedNs    int64       `json:"started_ns"`
	EndedNs      int64       `json:"ended_ns"`
	DurationNs   int64       `json:"duration_ns"`
	Attributes   []Attribute `json:"attributes,omitempty"`
}

// Trace is the set of spans sharing one trace ID, plus run-level metadata.
type Trace struct {
	TraceID    string `json:"trace_id"`
	RunLabel   string `json:"run_label"`
	Source     string `json:"source"` // "mark" | "otlp"
	RootName   string `json:"root_name"`
	StartedNs  int64  `json:"started_ns"`
	EndedNs    int64  `json:"ended_ns"`
	DurationNs int64  `json:"duration_ns"`
	SpanCount  int    `json:"span_count"`
	Spans      []Span `json:"spans"`
}

// TreeNode is a span positioned in the parent/child hierarchy, annotated with
// its depth from the root (the root has depth 0).
type TreeNode struct {
	Span     Span
	Depth    int
	Children []*TreeNode
}

// AssembleTree organizes spans into a parent/child tree rooted at the span with
// no parent, annotating each node with its depth. Sibling children are ordered
// by start time so rendering is deterministic. Returns nil when spans is empty
// or contains no root span.
//
// The function is defensive against malformed input, since both capture paths
// (marks and the OTLP receiver) can deliver untrusted span sets:
//   - Duplicate span IDs: the first occurrence wins; later ones are ignored.
//   - Multiple parentless spans: the first (in input order) is the root; v1
//     models a single-rooted trace, so additional roots are not attached.
//   - Orphans (parent absent from the input) and self-parented spans are
//     dropped rather than attached, so no cycle can be reached from the root.
func AssembleTree(spans []Span) *TreeNode {
	if len(spans) == 0 {
		return nil
	}

	// Build nodes in first-occurrence order, ignoring duplicate span IDs.
	nodes := make(map[string]*TreeNode, len(spans))
	order := make([]*TreeNode, 0, len(spans))
	for i := range spans {
		if _, exists := nodes[spans[i].SpanID]; exists {
			continue
		}
		node := &TreeNode{Span: spans[i]}
		nodes[spans[i].SpanID] = node
		order = append(order, node)
	}

	var root *TreeNode
	for _, node := range order {
		if node.Span.ParentSpanID == "" {
			if root == nil {
				root = node
			}
			continue
		}
		// Attach to the parent only when it exists and is not the node itself
		// (a self-parent would create a cycle).
		if parent, ok := nodes[node.Span.ParentSpanID]; ok && parent != node {
			parent.Children = append(parent.Children, node)
		}
	}

	if root == nil {
		return nil
	}
	assignDepth(root, 0)
	return root
}

// assignDepth sets depth on n and its descendants, ordering each node's children
// by start time before recursing.
func assignDepth(n *TreeNode, depth int) {
	n.Depth = depth
	sort.Slice(n.Children, func(i, j int) bool {
		return n.Children[i].Span.StartedNs < n.Children[j].Span.StartedNs
	})
	for _, child := range n.Children {
		assignDepth(child, depth+1)
	}
}
