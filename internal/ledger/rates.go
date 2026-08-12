package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var decimalRatePattern = regexp.MustCompile(`^(0|[0-9]+)(\.[0-9]+)?$`)

// RateFile is a user-authored, versioned set of exact provider/model rates.
// Decimal strings avoid floating-point ambiguity in deterministic reports.
type RateFile struct {
	Schema   string      `json:"schema"`
	AsOf     string      `json:"as_of"`
	Currency string      `json:"currency"`
	Rates    []RateEntry `json:"rates"`
}

// RateEntry matches one exact provider/model pair.
type RateEntry struct {
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	PerMillion RateValues `json:"per_million_tokens"`
}

// RateValues are decimal currency units per one million tokens. Specialized
// subset rates are optional and fall back to Input or Output in the same entry.
type RateValues struct {
	Input           string `json:"input"`
	Output          string `json:"output"`
	CacheReadInput  string `json:"cache_read_input,omitempty"`
	CacheWriteInput string `json:"cache_write_input,omitempty"`
	ReasoningOutput string `json:"reasoning_output,omitempty"`
}

// RateBook is a validated local rate file plus exact provenance.
type RateBook struct {
	file       RateFile
	provenance RateProvenance
}

// LoadRates reads and validates one local rate file. It never uses the network.
func LoadRates(path string) (*RateBook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ledger rates %q: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var file RateFile
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode ledger rates %q: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode ledger rates %q: trailing JSON value", path)
		}
		return nil, fmt.Errorf("decode ledger rates %q trailing data: %w", path, err)
	}
	if err := validateRateFile(file); err != nil {
		return nil, fmt.Errorf("validate ledger rates %q: %w", path, err)
	}
	digest := sha256.Sum256(data)
	return &RateBook{
		file: file,
		provenance: RateProvenance{
			Path: path, SHA256: hex.EncodeToString(digest[:]), Schema: file.Schema,
			AsOf: file.AsOf, Currency: file.Currency,
		},
	}, nil
}

func validateRateFile(file RateFile) error {
	if file.Schema != RateSchema {
		return fmt.Errorf("schema must be %q", RateSchema)
	}
	if _, err := time.Parse("2006-01-02", file.AsOf); err != nil {
		if _, timestampErr := time.Parse(time.RFC3339, file.AsOf); timestampErr != nil {
			return fmt.Errorf("as_of must be YYYY-MM-DD or RFC3339")
		}
	}
	if strings.TrimSpace(file.Currency) == "" {
		return fmt.Errorf("currency is required")
	}
	if len(file.Rates) == 0 {
		return fmt.Errorf("at least one rate entry is required")
	}
	seen := make(map[string]struct{}, len(file.Rates))
	for i, entry := range file.Rates {
		if strings.TrimSpace(entry.Provider) == "" || strings.TrimSpace(entry.Model) == "" {
			return fmt.Errorf("rates[%d] requires provider and model", i)
		}
		key := entry.Provider + "\x00" + entry.Model
		if _, exists := seen[key]; exists {
			return fmt.Errorf("rates[%d] duplicates provider/model", i)
		}
		seen[key] = struct{}{}
		for name, value := range map[string]string{
			"input":             entry.PerMillion.Input,
			"output":            entry.PerMillion.Output,
			"cache_read_input":  entry.PerMillion.CacheReadInput,
			"cache_write_input": entry.PerMillion.CacheWriteInput,
			"reasoning_output":  entry.PerMillion.ReasoningOutput,
		} {
			if (name == "input" || name == "output") && value == "" {
				return fmt.Errorf("rates[%d].%s is required", i, name)
			}
			if value == "" {
				continue
			}
			if !decimalRatePattern.MatchString(value) {
				return fmt.Errorf("rates[%d].%s must be a non-negative decimal string", i, name)
			}
			rate, ok := new(big.Rat).SetString(value)
			if !ok || rate.Sign() < 0 {
				return fmt.Errorf("rates[%d].%s must be a non-negative decimal string", i, name)
			}
		}
	}
	return nil
}

// ApplyRates adds an estimate to report using only book's explicit entries.
// Any missing or ambiguous input leaves the estimate UNKNOWN.
func ApplyRates(report *Report, book *RateBook) {
	if report == nil || book == nil {
		return
	}
	estimate := Estimate{Status: "unknown", Provenance: book.provenance}
	total := new(big.Rat)
	pricedUsage := false
	for _, row := range report.Rows {
		if !hasObservedUsage(row.Observed) {
			continue
		}
		if !row.Contributes {
			if row.Mode == "rollup" {
				continue
			}
			estimate.Reason = row.SpanID + ": usage observation is excluded from contributions"
			report.Summary.Estimate = &estimate
			return
		}
		pricedUsage = true
		entry, ok := book.match(row.Provider, row.Model)
		if !ok {
			estimate.Reason = "missing rate for " + row.Provider + "/" + row.Model
			report.Summary.Estimate = &estimate
			return
		}
		amount, reason := estimateUsage(row.Contribution, entry.PerMillion)
		if reason != "" {
			estimate.Reason = row.SpanID + ": " + reason
			report.Summary.Estimate = &estimate
			return
		}
		total.Add(total, amount)
	}
	if !pricedUsage {
		estimate.Reason = "no estimable usage observations"
		report.Summary.Estimate = &estimate
		return
	}
	amount := formatRat(total)
	estimate.Status = "known"
	estimate.Amount = &amount
	report.Summary.Estimate = &estimate
}

func (book *RateBook) match(provider, modelName string) (RateEntry, bool) {
	for _, entry := range book.file.Rates {
		if entry.Provider == provider && entry.Model == modelName {
			return entry, true
		}
	}
	return RateEntry{}, false
}

func estimateUsage(usage Usage, rates RateValues) (*big.Rat, string) {
	counts := []Count{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning}
	for _, count := range counts {
		if !isKnown(count) {
			return nil, "usage components are unknown"
		}
	}
	cacheTotal, overflow := checkedAdd(*usage.CacheRead.Value, *usage.CacheWrite.Value)
	if overflow || cacheTotal > *usage.Input.Value {
		return nil, "cache subsets exceed input"
	}
	if *usage.Reasoning.Value > *usage.Output.Value {
		return nil, "reasoning subset exceeds output"
	}
	uncached := *usage.Input.Value - cacheTotal
	nonReasoning := *usage.Output.Value - *usage.Reasoning.Value
	cacheReadRate := rates.CacheReadInput
	if cacheReadRate == "" {
		cacheReadRate = rates.Input
	}
	cacheWriteRate := rates.CacheWriteInput
	if cacheWriteRate == "" {
		cacheWriteRate = rates.Input
	}
	reasoningRate := rates.ReasoningOutput
	if reasoningRate == "" {
		reasoningRate = rates.Output
	}
	parts := []struct {
		count int64
		rate  string
	}{
		{uncached, rates.Input},
		{*usage.CacheRead.Value, cacheReadRate},
		{*usage.CacheWrite.Value, cacheWriteRate},
		{nonReasoning, rates.Output},
		{*usage.Reasoning.Value, reasoningRate},
	}
	total := new(big.Rat)
	for _, part := range parts {
		rate, ok := new(big.Rat).SetString(part.rate)
		if !ok {
			return nil, "validated rate could not be parsed"
		}
		term := new(big.Rat).Mul(rate, big.NewRat(part.count, 1_000_000))
		total.Add(total, term)
	}
	return total, ""
}

func hasObservedUsage(usage Usage) bool {
	counts := []Count{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning, usage.ToolInput}
	for _, count := range counts {
		if len(count.Sources) > 0 {
			return true
		}
	}
	return false
}

func formatRat(value *big.Rat) string {
	out := value.FloatString(9)
	out = strings.TrimRight(out, "0")
	out = strings.TrimRight(out, ".")
	if out == "" || out == "-0" {
		return "0"
	}
	return out
}

// SortedRateKeys returns stable provider/model identifiers for diagnostics and
// tests without exposing mutable RateBook internals.
func (book *RateBook) SortedRateKeys() []string {
	if book == nil {
		return nil
	}
	keys := make([]string, 0, len(book.file.Rates))
	for _, entry := range book.file.Rates {
		keys = append(keys, entry.Provider+"/"+entry.Model)
	}
	sort.Strings(keys)
	return keys
}
