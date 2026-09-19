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

// Oracle is the ground-truth impact set for a PR: the packages whose tests
// fail because of the PR's source change. Method "revert" restores the
// PR's non-test source files to base while keeping its tests, runs the
// suite, and takes the failures that do not also fail at head. Method
// "head_failures" is the fallback: packages failing at head.
type Oracle struct {
	Ran       bool     `json:"ran"`
	Method    string   `json:"method"`
	Failed    []string `json:"failed"`
	Noise     []string `json:"noise,omitempty"` // failing at head too; excluded from Failed
	RuntimeMS int64    `json:"runtime_ms"`
}

// RevertOracle derives the impact set from a revert run and the head run.
// Only packages that had tests at head (non-skipped head result) can be in
// the impact set: a package the PR added has no base version, and a package
// without tests cannot be "missed".
func RevertOracle(revert, head map[string]PackageResult) Oracle {
	o := Oracle{Ran: true, Method: "revert", Failed: []string{}}
	for ip, r := range revert {
		h, ok := head[ip]
		if r.Skipped || !ok || h.Skipped {
			continue
		}
		o.RuntimeMS += r.Elapsed.Milliseconds()
		if r.Passed {
			continue
		}
		if !h.Passed {
			o.Noise = append(o.Noise, ip)
			continue
		}
		o.Failed = append(o.Failed, ip)
	}
	sort.Strings(o.Failed)
	sort.Strings(o.Noise)
	return o
}

// HeadOracle is the fallback ground truth: packages failing at head.
func HeadOracle(full FullSuite) Oracle {
	return Oracle{Ran: full.Ran, Method: "head_failures", Failed: append([]string{}, full.Failed...)}
}

// RestrictToTested narrows a selection to the packages the suite actually
// ran (non-skipped results), so reduction is measured over the tested
// universe rather than over every discovered test package.
func RestrictToTested(sel Selection, results map[string]PackageResult) Selection {
	if results == nil {
		return sel
	}
	out := sel
	out.Selected = []string{}
	out.TargetsTotal = 0
	for _, r := range results {
		if !r.Skipped {
			out.TargetsTotal++
		}
	}
	for _, ip := range sel.Selected {
		if r, ok := results[ip]; ok && !r.Skipped {
			out.Selected = append(out.Selected, ip)
		}
	}
	sort.Strings(out.Selected)
	return out
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
	Recall                  *float64 `json:"recall"` // nil when the oracle had no failures
	PlanMS                  int64    `json:"plan_ms"`
	JevLatencyMS            int64    `json:"jev_latency_ms"`
	JevInputTokens          int      `json:"jev_input_tokens"`
	JevRequests             int      `json:"jev_requests"`
	JevCostUSD              float64  `json:"jev_cost_usd"`
	Error                   string   `json:"error,omitempty"`
}

// Record is one line of results.jsonl: one PR, all strategies.
type Record struct {
	Repo         string            `json:"repo"`
	PR           int               `json:"pr"`
	Title        string            `json:"title,omitempty"`
	Base         string            `json:"base"`
	Head         string            `json:"head"`
	FullSuite    FullSuite         `json:"full_suite"`
	Oracle       Oracle            `json:"oracle"`
	Strategies   []StrategyMetrics `json:"strategies"`
	Error        string            `json:"error,omitempty"`
	ArtifactsDir string            `json:"artifacts_dir,omitempty"`
	Evaluation   string            `json:"evaluation,omitempty"`
	SourceFiles  []string          `json:"source_files,omitempty"`
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

// Compute turns a selection plus the full-suite results and the oracle into
// metrics. results may be nil when the suite was not run; runtime and
// recall fields are then left at zero/nil. Recall is measured against
// oracle.Failed.
func Compute(sel Selection, full FullSuite, oracle Oracle, results map[string]PackageResult) StrategyMetrics {
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
	if !oracle.Ran {
		return m
	}
	for _, ip := range oracle.Failed {
		if selected[ip] {
			m.FailedDetected = append(m.FailedDetected, ip)
		} else {
			m.FailedMissed = append(m.FailedMissed, ip)
		}
	}
	if len(oracle.Failed) > 0 {
		r := float64(len(m.FailedDetected)) / float64(len(oracle.Failed))
		m.Recall = &r
	}
	return m
}

// StrategySummary aggregates one strategy across all records.
type StrategySummary struct {
	Evaluation              string   `json:"evaluation"`
	Strategy                string   `json:"strategy"`
	PRs                     int      `json:"prs"`
	MeanReductionPercent    float64  `json:"mean_reduction_percent"`
	MeanRuntimeReductionPct float64  `json:"mean_runtime_reduction_percent"`
	FailedTotal             int      `json:"failed_total"`
	FailedDetected          int      `json:"failed_detected"`
	FailedMissed            int      `json:"failed_missed"`
	Recall                  *float64 `json:"recall"` // detected/total; nil when total==0
	PRsWithFailures         int      `json:"prs_with_failures"`
	MeanJevLatencyMS        float64  `json:"mean_jev_latency_ms"`
	TotalJevCostUSD         float64  `json:"total_jev_cost_usd"`
	Errors                  int      `json:"errors"`
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
			evaluation := evaluationName(rec.Evaluation)
			key := evaluation + "\x00" + sm.Strategy
			a, ok := accs[key]
			if !ok {
				a = &acc{s: StrategySummary{Evaluation: evaluation, Strategy: sm.Strategy}}
				accs[key] = a
				order = append(order, key)
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
			if len(rec.Oracle.Failed) > 0 {
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
		if s.FailedTotal > 0 {
			recall := float64(s.FailedDetected) / float64(s.FailedTotal)
			s.Recall = &recall
		}
		out = append(out, s)
	}
	return out
}

func evaluationName(name string) string {
	if name == "" {
		return "historical_pr"
	}
	return name
}

func round1(f float64) float64 {
	if f < 0 {
		return -float64(int(-f*10+0.5)) / 10
	}
	return float64(int(f*10+0.5)) / 10
}
