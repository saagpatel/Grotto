package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/saagpatel/grotto/internal/model"
)

// CriticalPathSchema is the versioned machine-readable critical-path contract.
const CriticalPathSchema = "grotto.critical_path.v1"

// CriticalPathClaimCeiling is the proof limit encoded in every v1 report.
const CriticalPathClaimCeiling = "longest-duration chain through stored cargo.unit/cargo.unblocks edges; no path is emitted for no-edge, missing, cyclic, or malformed graphs"

const (
	attrCargoUnit     = "cargo.unit"
	attrCargoUnblocks = "cargo.unblocks"

	criticalPathStatusOK        = "ok"
	criticalPathStatusNoEdge    = "no_edge"
	criticalPathStatusMissing   = "missing"
	criticalPathStatusCyclic    = "cyclic"
	criticalPathStatusMalformed = "malformed"

	issueNoEdge              = "no_edge"
	issueMissingUnit         = "missing_unit"
	issueCycle               = "cycle"
	issueInvalidUnit         = "invalid_unit"
	issueDuplicateUnit       = "duplicate_unit"
	issueInvalidUnblocks     = "invalid_unblocks"
	issueUnblocksWithoutUnit = "unblocks_without_unit"
)

// CriticalPathIssue is one deterministic graph diagnostic.
type CriticalPathIssue struct {
	Code    string `json:"code"`
	SpanID  string `json:"span_id,omitempty"`
	Unit    *int   `json:"unit,omitempty"`
	Message string `json:"message"`
}

// CriticalPathSpan is one ordered node on an emitted critical path.
type CriticalPathSpan struct {
	Index      int    `json:"index"`
	Unit       int    `json:"unit"`
	SpanID     string `json:"span_id"`
	Name       string `json:"name"`
	StartedNs  int64  `json:"started_ns"`
	EndedNs    int64  `json:"ended_ns"`
	DurationNs int64  `json:"duration_ns"`
}

// CriticalPathMetrics are path and graph totals derived from stored spans.
// CriticalNs is null unless status is ok.
type CriticalPathMetrics struct {
	CriticalNs    *int64 `json:"critical_ns"`
	TotalWorkNs   int64  `json:"total_work_ns"`
	WallClockNs   int64  `json:"wall_clock_ns"`
	UnitCount     int    `json:"unit_count"`
	PathSpanCount int    `json:"path_span_count"`
	EdgeCount     int    `json:"edge_count"`
}

// CriticalPathReport is the versioned deterministic critical-path projection.
type CriticalPathReport struct {
	Schema       string              `json:"schema"`
	TraceID      string              `json:"trace_id"`
	RunLabel     string              `json:"run_label"`
	Source       string              `json:"source"`
	Status       string              `json:"status"`
	Spans        []CriticalPathSpan  `json:"spans"`
	Metrics      CriticalPathMetrics `json:"metrics"`
	Issues       []CriticalPathIssue `json:"issues"`
	ClaimCeiling string              `json:"claim_ceiling"`
}

type cargoNode struct {
	unit int
	span model.Span
}

type cargoGraph struct {
	nodes     map[int]cargoNode
	order     []int
	succs     map[int][]int
	preds     map[int][]int
	issues    []CriticalPathIssue
	edgeN     int
	malformed bool
	missing   bool
	cyclic    bool
}

// AnalyzeCriticalPath projects a stored trace onto grotto.critical_path.v1.
// The report is derived only from cargo.unit / cargo.unblocks attributes. It
// never invents a path for no-edge, missing, cyclic, or malformed graphs.
func AnalyzeCriticalPath(tr model.Trace) CriticalPathReport {
	report := CriticalPathReport{
		Schema:       CriticalPathSchema,
		TraceID:      tr.TraceID,
		RunLabel:     tr.RunLabel,
		Source:       tr.Source,
		Spans:        make([]CriticalPathSpan, 0),
		Issues:       make([]CriticalPathIssue, 0),
		ClaimCeiling: CriticalPathClaimCeiling,
		Metrics: CriticalPathMetrics{
			WallClockNs: tr.DurationNs,
		},
	}

	g := buildCargoGraph(tr.Spans)
	report.Issues = g.issues
	report.Metrics.UnitCount = len(g.nodes)
	report.Metrics.EdgeCount = g.edgeN
	for _, unit := range g.order {
		report.Metrics.TotalWorkNs += g.nodes[unit].span.DurationNs
	}

	switch {
	case g.malformed:
		report.Status = criticalPathStatusMalformed
	case g.cyclic:
		report.Status = criticalPathStatusCyclic
	case g.missing:
		report.Status = criticalPathStatusMissing
	case len(g.nodes) == 0:
		report.Status = criticalPathStatusNoEdge
		report.Issues = append(report.Issues, CriticalPathIssue{
			Code:    issueNoEdge,
			Message: "no cargo.unit attributes; trace has no reconstructable dependency graph",
		})
	default:
		report.Status = criticalPathStatusOK
		spans, criticalNs := longestPath(g)
		report.Spans = spans
		report.Metrics.CriticalNs = int64Ptr(criticalNs)
		report.Metrics.PathSpanCount = len(spans)
	}

	sortCriticalPathIssues(report.Issues)
	return report
}

// WriteCriticalPathJSON writes the versioned report with stable indentation.
func WriteCriticalPathJSON(w io.Writer, report CriticalPathReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode critical-path json: %w", err)
	}
	return nil
}

// ValidateCriticalPathReport enforces the executable invariants paired with
// schemas/critical-path-v1.schema.json. It is dependency-free so validation
// cannot add a second runtime or network surface.
func ValidateCriticalPathReport(report CriticalPathReport) error {
	if report.Schema != CriticalPathSchema {
		return fmt.Errorf("schema = %q, want %q", report.Schema, CriticalPathSchema)
	}
	if report.ClaimCeiling != CriticalPathClaimCeiling {
		return fmt.Errorf("claim_ceiling = %q, want %q", report.ClaimCeiling, CriticalPathClaimCeiling)
	}
	if report.TraceID == "" {
		return fmt.Errorf("trace_id is required")
	}
	switch report.Status {
	case criticalPathStatusOK, criticalPathStatusNoEdge, criticalPathStatusMissing, criticalPathStatusCyclic, criticalPathStatusMalformed:
	default:
		return fmt.Errorf("status = %q is not a v1 graph classification", report.Status)
	}
	if report.Spans == nil {
		return fmt.Errorf("spans must be an array")
	}
	if report.Issues == nil {
		return fmt.Errorf("issues must be an array")
	}
	if report.Metrics.UnitCount < 0 || report.Metrics.PathSpanCount < 0 || report.Metrics.EdgeCount < 0 {
		return fmt.Errorf("metrics counts must be non-negative")
	}
	if report.Metrics.PathSpanCount != len(report.Spans) {
		return fmt.Errorf("path_span_count=%d does not match spans=%d", report.Metrics.PathSpanCount, len(report.Spans))
	}
	if report.Status == criticalPathStatusOK {
		if report.Metrics.CriticalNs == nil {
			return fmt.Errorf("ok report requires critical_ns")
		}
		if len(report.Spans) == 0 {
			return fmt.Errorf("ok report requires a non-empty path")
		}
		var sum int64
		for i, span := range report.Spans {
			if span.Index != i {
				return fmt.Errorf("spans[%d].index = %d, want %d", i, span.Index, i)
			}
			if span.SpanID == "" {
				return fmt.Errorf("spans[%d].span_id is required", i)
			}
			if span.DurationNs < 0 {
				return fmt.Errorf("spans[%d].duration_ns is negative", i)
			}
			sum += span.DurationNs
		}
		if sum != *report.Metrics.CriticalNs {
			return fmt.Errorf("critical_ns=%d does not equal path duration sum %d", *report.Metrics.CriticalNs, sum)
		}
	} else {
		if report.Metrics.CriticalNs != nil {
			return fmt.Errorf("%s report must not invent critical_ns", report.Status)
		}
		if len(report.Spans) != 0 {
			return fmt.Errorf("%s report must not invent a path", report.Status)
		}
	}
	for i := 1; i < len(report.Issues); i++ {
		if criticalPathIssueLess(report.Issues[i], report.Issues[i-1]) {
			return fmt.Errorf("issues are not deterministically ordered at index %d", i)
		}
	}
	return nil
}

func buildCargoGraph(spans []model.Span) cargoGraph {
	g := cargoGraph{
		nodes:  make(map[int]cargoNode),
		succs:  make(map[int][]int),
		preds:  make(map[int][]int),
		issues: make([]CriticalPathIssue, 0),
	}

	type pendingEdges struct {
		spanID  string
		unit    int
		targets []int
	}
	pending := make([]pendingEdges, 0, len(spans))

	for _, s := range spans {
		unit, hasUnit, unitErr := parseCargoUnit(s)
		unblocks, badTokens, hasUnblocks := parseCargoUnblocks(s)
		owner := false

		if unitErr != nil {
			g.malformed = true
			g.issues = append(g.issues, CriticalPathIssue{
				Code:    issueInvalidUnit,
				SpanID:  s.SpanID,
				Message: unitErr.Error(),
			})
		} else if hasUnit {
			if existing, dup := g.nodes[unit]; dup {
				g.malformed = true
				g.issues = append(g.issues, CriticalPathIssue{
					Code:    issueDuplicateUnit,
					SpanID:  s.SpanID,
					Unit:    intPtr(unit),
					Message: fmt.Sprintf("cargo.unit %d duplicated (first span %s)", unit, existing.span.SpanID),
				})
			} else {
				g.nodes[unit] = cargoNode{unit: unit, span: s}
				owner = true
			}
		}

		if hasUnblocks && len(badTokens) > 0 {
			g.malformed = true
			for _, tok := range badTokens {
				g.issues = append(g.issues, CriticalPathIssue{
					Code:    issueInvalidUnblocks,
					SpanID:  s.SpanID,
					Message: fmt.Sprintf("cargo.unblocks token %q is not a non-negative integer", tok),
				})
			}
		}

		if hasUnblocks && !owner {
			if !hasUnit || unitErr != nil {
				g.malformed = true
				g.issues = append(g.issues, CriticalPathIssue{
					Code:    issueUnblocksWithoutUnit,
					SpanID:  s.SpanID,
					Message: "cargo.unblocks present without a valid cargo.unit",
				})
			}
			continue
		}
		if hasUnblocks && owner {
			pending = append(pending, pendingEdges{spanID: s.SpanID, unit: unit, targets: unblocks})
		}
	}

	g.order = make([]int, 0, len(g.nodes))
	for unit := range g.nodes {
		g.order = append(g.order, unit)
	}
	sort.Ints(g.order)

	seenEdge := make(map[[2]int]struct{})
	for _, edge := range pending {
		if _, ok := g.nodes[edge.unit]; !ok {
			continue
		}
		for _, target := range edge.targets {
			if target == edge.unit {
				g.cyclic = true
				g.issues = append(g.issues, CriticalPathIssue{
					Code:    issueCycle,
					SpanID:  edge.spanID,
					Unit:    intPtr(edge.unit),
					Message: fmt.Sprintf("self-edge on unit %d", edge.unit),
				})
				continue
			}
			if _, ok := g.nodes[target]; !ok {
				g.missing = true
				g.issues = append(g.issues, CriticalPathIssue{
					Code:    issueMissingUnit,
					SpanID:  edge.spanID,
					Unit:    intPtr(target),
					Message: fmt.Sprintf("unit %d unblocks missing unit %d", edge.unit, target),
				})
				continue
			}
			key := [2]int{edge.unit, target}
			if _, dup := seenEdge[key]; dup {
				continue
			}
			seenEdge[key] = struct{}{}
			g.succs[edge.unit] = append(g.succs[edge.unit], target)
			g.preds[target] = append(g.preds[target], edge.unit)
			g.edgeN++
		}
	}

	for _, unit := range g.order {
		sort.Ints(g.succs[unit])
		sort.Ints(g.preds[unit])
	}

	if cycleUnits := findCycleUnits(g); len(cycleUnits) > 0 {
		g.cyclic = true
		ids := make([]string, len(cycleUnits))
		for i, unit := range cycleUnits {
			ids[i] = strconv.Itoa(unit)
		}
		g.issues = append(g.issues, CriticalPathIssue{
			Code:    issueCycle,
			Message: "cycle involves units " + strings.Join(ids, ","),
		})
	}

	return g
}

func parseCargoUnit(s model.Span) (int, bool, error) {
	for _, a := range s.Attributes {
		if a.Key != attrCargoUnit {
			continue
		}
		i, err := strconv.Atoi(strings.TrimSpace(a.Value))
		if err != nil || i < 0 {
			return 0, true, fmt.Errorf("cargo.unit value %q is not a non-negative integer", a.Value)
		}
		return i, true, nil
	}
	return 0, false, nil
}

func parseCargoUnblocks(s model.Span) (targets []int, bad []string, present bool) {
	for _, a := range s.Attributes {
		if a.Key != attrCargoUnblocks {
			continue
		}
		present = true
		if strings.TrimSpace(a.Value) == "" {
			return nil, []string{a.Value}, true
		}
		parts := strings.Split(a.Value, ",")
		seen := make(map[int]struct{})
		for _, part := range parts {
			tok := strings.TrimSpace(part)
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 {
				bad = append(bad, tok)
				continue
			}
			if _, dup := seen[i]; dup {
				continue
			}
			seen[i] = struct{}{}
			targets = append(targets, i)
		}
		return targets, bad, true
	}
	return nil, nil, false
}

func findCycleUnits(g cargoGraph) []int {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[int]int, len(g.nodes))
	onCycle := make(map[int]struct{})
	var stack []int

	var visit func(int)
	visit = func(unit int) {
		color[unit] = gray
		stack = append(stack, unit)
		for _, next := range g.succs[unit] {
			switch color[next] {
			case white:
				visit(next)
			case gray:
				for i := len(stack) - 1; i >= 0; i-- {
					onCycle[stack[i]] = struct{}{}
					if stack[i] == next {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[unit] = black
	}

	for _, unit := range g.order {
		if color[unit] == white {
			visit(unit)
		}
	}
	if len(onCycle) == 0 {
		return nil
	}
	out := make([]int, 0, len(onCycle))
	for unit := range onCycle {
		out = append(out, unit)
	}
	sort.Ints(out)
	return out
}

func longestPath(g cargoGraph) ([]CriticalPathSpan, int64) {
	finish := make(map[int]int64, len(g.nodes))
	back := make(map[int]int, len(g.nodes))

	var longest func(int) int64
	longest = func(unit int) int64 {
		if f, done := finish[unit]; done {
			return f
		}
		best := int64(-1)
		bp := -1
		for _, p := range g.preds[unit] {
			f := longest(p)
			if f > best || (f == best && (bp == -1 || p < bp)) {
				best = f
				bp = p
			}
		}
		if best < 0 {
			best = 0
		}
		finish[unit] = best + g.nodes[unit].span.DurationNs
		back[unit] = bp
		return finish[unit]
	}

	end := -1
	var endFinish int64
	for _, unit := range g.order {
		f := longest(unit)
		if end == -1 || f > endFinish || (f == endFinish && unit < end) {
			end = unit
			endFinish = f
		}
	}

	seen := make(map[int]struct{}, len(g.nodes))
	var rev []int
	for cur := end; cur != -1; cur = back[cur] {
		if _, dup := seen[cur]; dup {
			break
		}
		seen[cur] = struct{}{}
		rev = append(rev, cur)
	}

	spans := make([]CriticalPathSpan, len(rev))
	for i := range rev {
		unit := rev[len(rev)-1-i]
		n := g.nodes[unit]
		spans[i] = CriticalPathSpan{
			Index:      i,
			Unit:       unit,
			SpanID:     n.span.SpanID,
			Name:       n.span.Name,
			StartedNs:  n.span.StartedNs,
			EndedNs:    n.span.EndedNs,
			DurationNs: n.span.DurationNs,
		}
	}
	return spans, endFinish
}

func sortCriticalPathIssues(issues []CriticalPathIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return criticalPathIssueLess(issues[i], issues[j])
	})
}

func criticalPathIssueLess(a, b CriticalPathIssue) bool {
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	if a.SpanID != b.SpanID {
		return a.SpanID < b.SpanID
	}
	au, bu := issueUnit(a), issueUnit(b)
	if au != bu {
		return au < bu
	}
	return a.Message < b.Message
}

func issueUnit(issue CriticalPathIssue) int {
	if issue.Unit == nil {
		return -1
	}
	return *issue.Unit
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }
