// Package benchmark measures JevCI against baselines on historical pull
// requests. This file defines the metric schema and the computations; the
// rest of the package is plumbing (clone, checkout, go test -json, planning).
package benchmark

import (
	"sort"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// PricePerMillionInputTokensUSD re-exports the single source of truth in
// internal/semantic (Jev's published jev-1.13.0 input price).
const PricePerMillionInputTokensUSD = semantic.PricePerMillionInputTokensUSD

// PackageResult is the outcome of one package in a `go test -json` run.
type PackageResult struct {
	ImportPath  string        `json:"import_path"`
	Passed      bool          `json:"passed"`
	Skipped     bool          `json:"skipped"` // no test files / [no tests to run]
	Elapsed     time.Duration `json:"elapsed_ns"`
	FailedTests []string      `json:"failed_tests"`
}

// Selection is what one strategy chose for one PR.
type Selection struct {
	Strategy       string   `json:"strategy"`
	Selected       []string `json:"selected"` // import paths
	TargetsTotal   int      `json:"targets_total"`
	PlanMS         int64    `json:"plan_ms"`
	JevLatencyMS   int64    `json:"jev_latency_ms"`
	JevInputTokens int      `json:"jev_input_tokens"`
	JevRequests    int      `json:"jev_requests"`
	JevModel       string   `json:"jev_model"`
	Error          string   `json:"error,omitempty"`
}

// FullSuite summarises the full `go test ./...` run at the PR head.
type FullSuite struct {
	Ran       bool     `json:"ran"`
	Packages  int      `json:"packages"` // packages with tests that ran
	Failed    []string `json:"failed"`   // import paths of failed packages
	RuntimeMS int64    `json:"runtime_ms"`
}

// StrategyMetrics are the per-PR numbers for one strategy.
type StrategyMetrics struct {
	Strategy                string   `json:"strategy"`
	TargetsTotal            int      `json:"targets_total"`
	Selected                int      `json:"selected"`
	ReductionPercent        float64  `json:"reduction_percent"`
	SelectedRuntimeMS       int64    `json:"selected_runtime_ms"` // serial sum of selected package times
	RuntimeReductionPercent float64  `json:"runtime_reduction_percent"`
	FailedDetected          []string `json:"failed_detected"`
	FailedMissed            []string `json:"failed_missed"`
	Recall                  *float64 `json:"recall"` // nil when the full suite had no failures
	PlanMS                  int64    `json:"plan_ms"`
	JevLatencyMS            int64    `json:"jev_latency_ms"`
	JevInputTokens          int      `json:"jev_input_tokens"`
	JevRequests             int      `json:"jev_requests"`
	JevCostUSD              float64  `json:"jev_cost_usd"`
	Error                   string   `json:"error,omitempty"`
}

// Record is one line of results.jsonl: one PR, all strategies.
type Record struct {
	Repo       string            `json:"repo"`
	PR         int               `json:"pr"`
	Title      string            `json:"title,omitempty"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	FullSuite  FullSuite         `json:"full_suite"`
	Strategies []StrategyMetrics `json:"strategies"`
	Error      string            `json:"error,omitempty"`
}

// SummarizeFullSuite derives FullSuite from per-package results. Packages
// without tests are excluded from the count; runtime is the serial sum.
func SummarizeFullSuite(results map[string]PackageResult) FullSuite {
	fs := FullSuite{Ran: true, Failed: []string{}}
	for ip, r := range results {
		if r.Skipped {
			continue
		}
		fs.Packages++
		fs.RuntimeMS += r.Elapsed.Milliseconds()
		if !r.Passed {
			fs.Failed = append(fs.Failed, ip)
		}
	}
	sort.Strings(fs.Failed)
	return fs
}

// Compute turns a selection plus the full-suite results into metrics.
// results may be nil when the suite was not run; runtime and recall fields
// are then left at zero/nil.
func Compute(sel Selection, full FullSuite, results map[string]PackageResult) StrategyMetrics {
	m := StrategyMetrics{
		Strategy: sel.Strategy, TargetsTotal: sel.TargetsTotal, Selected: len(sel.Selected),
		PlanMS: sel.PlanMS, JevLatencyMS: sel.JevLatencyMS, JevInputTokens: sel.JevInputTokens,
		JevRequests: sel.JevRequests, Error: sel.Error,
		FailedDetected: []string{}, FailedMissed: []string{},
	}
	m.JevCostUSD = float64(sel.JevInputTokens) / 1e6 * PricePerMillionInputTokensUSD
	if sel.TargetsTotal > 0 {
		m.ReductionPercent = round1((1 - float64(m.Selected)/float64(sel.TargetsTotal)) * 100)
	}
	if !full.Ran {
		return m
	}
	selected := map[string]bool{}
	for _, ip := range sel.Selected {
		selected[ip] = true
		if r, ok := results[ip]; ok && !r.Skipped {
			m.SelectedRuntimeMS += r.Elapsed.Milliseconds()
		}
	}
	if full.RuntimeMS > 0 {
		m.RuntimeReductionPercent = round1((1 - float64(m.SelectedRuntimeMS)/float64(full.RuntimeMS)) * 100)
	}
	for _, ip := range full.Failed {
		if selected[ip] {
			m.FailedDetected = append(m.FailedDetected, ip)
		} else {
			m.FailedMissed = append(m.FailedMissed, ip)
		}
	}
	if len(full.Failed) > 0 {
		r := float64(len(m.FailedDetected)) / float64(len(full.Failed))
		m.Recall = &r
	}
	return m
}

// StrategySummary aggregates one strategy across all records.
type StrategySummary struct {
	Strategy                string  `json:"strategy"`
	PRs                     int     `json:"prs"`
	MeanReductionPercent    float64 `json:"mean_reduction_percent"`
	MeanRuntimeReductionPct float64 `json:"mean_runtime_reduction_percent"`
	FailedTotal             int     `json:"failed_total"`
	FailedDetected          int     `json:"failed_detected"`
	FailedMissed            int     `json:"failed_missed"`
	Recall                  float64 `json:"recall"` // detected/total; 1 when total==0 (nothing to miss)
	PRsWithFailures         int     `json:"prs_with_failures"`
	MeanJevLatencyMS        float64 `json:"mean_jev_latency_ms"`
	TotalJevCostUSD         float64 `json:"total_jev_cost_usd"`
	Errors                  int     `json:"errors"`
}

// Aggregate computes per-strategy summaries over records that planned
// successfully. Records with a top-level Error are skipped entirely.
func Aggregate(records []Record) []StrategySummary {
	type acc struct {
		s                 StrategySummary
		sumRed, sumRtRed  float64
		sumLat            float64
		runtimeMeasurable int
	}
	accs := map[string]*acc{}
	var order []string
	for _, rec := range records {
		if rec.Error != "" {
			continue
		}
		for _, sm := range rec.Strategies {
			a, ok := accs[sm.Strategy]
			if !ok {
				a = &acc{s: StrategySummary{Strategy: sm.Strategy}}
				accs[sm.Strategy] = a
				order = append(order, sm.Strategy)
			}
			if sm.Error != "" {
				a.s.Errors++
			}
			a.s.PRs++
			a.sumRed += sm.ReductionPercent
			if rec.FullSuite.Ran && rec.FullSuite.RuntimeMS > 0 {
				a.sumRtRed += sm.RuntimeReductionPercent
				a.runtimeMeasurable++
			}
			a.sumLat += float64(sm.JevLatencyMS)
			a.s.TotalJevCostUSD += sm.JevCostUSD
			a.s.FailedDetected += len(sm.FailedDetected)
			a.s.FailedMissed += len(sm.FailedMissed)
			if len(rec.FullSuite.Failed) > 0 {
				a.s.PRsWithFailures++
			}
		}
	}
	out := make([]StrategySummary, 0, len(accs))
	for _, name := range order {
		a := accs[name]
		s := a.s
		if s.PRs > 0 {
			s.MeanReductionPercent = round1(a.sumRed / float64(s.PRs))
			s.MeanJevLatencyMS = round1(a.sumLat / float64(s.PRs))
		}
		if a.runtimeMeasurable > 0 {
			s.MeanRuntimeReductionPct = round1(a.sumRtRed / float64(a.runtimeMeasurable))
		}
		s.FailedTotal = s.FailedDetected + s.FailedMissed
		s.Recall = 1
		if s.FailedTotal > 0 {
			s.Recall = float64(s.FailedDetected) / float64(s.FailedTotal)
		}
		out = append(out, s)
	}
	return out
}

func round1(f float64) float64 {
	if f < 0 {
		return -float64(int(-f*10+0.5)) / 10
	}
	return float64(int(f*10+0.5)) / 10
}
